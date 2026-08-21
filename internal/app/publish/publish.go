// Package publish is the application-layer service behind `pf publish`'s
// self-contained production-evidence business rules: evaluating a
// publication policy against generated evidence, building the native
// SBOM/provenance/signature artifacts, and recording a workload's
// publish-lifecycle transition. cmd/platform-factory/lifecycle.go's
// runPublish still owns the CLI-facing orchestration (flag parsing, the
// operation-journal claim/complete/fail sequence shared with
// deploy/rollback/microvm, and the registry push itself - all genuine
// cross-cutting CLI infrastructure, not publish-domain logic) and calls
// into this package for the parts that are.
package publish

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CYPT71/platform-factory/internal/app/sbom"
	"github.com/CYPT71/platform-factory/internal/attestation"
	"github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/policy"
	provenancegen "github.com/CYPT71/platform-factory/internal/provenance"
	"github.com/CYPT71/platform-factory/internal/registry"
	"github.com/CYPT71/platform-factory/internal/signing"
	"github.com/CYPT71/platform-factory/internal/strictjson"
	"github.com/CYPT71/platform-factory/internal/workloadstate"
)

// EvaluatePolicy decodes the policy rules and evidence documents at
// policyPath/evidencePath, overlays the digest/SBOM/provenance/signature
// facts this specific publish call actually measured, and evaluates the
// resulting decision.
func EvaluatePolicy(policyPath, evidencePath string, published registry.Result, hasSBOM, hasProvenance, hasSignature bool) (policy.Decision, error) {
	rules, evidence, err := policy.DecodeRulesAndEvidence(policyPath, evidencePath)
	if err != nil {
		return policy.Decision{}, err
	}
	evidence.SubjectDigest = published.Digest
	evidence.SBOM = hasSBOM
	evidence.Provenance = hasProvenance
	evidence.Signature = hasSignature
	return policy.Evaluate(rules, evidence)
}

// Artifact is one linked OCI subject artifact BuildArtifacts produced -
// an SBOM, provenance predicate, or signature envelope - ready to push
// alongside the published manifest.
type Artifact struct {
	Name         string
	ArtifactType string
	PayloadType  string
	Payload      []byte
}

// BuildArtifacts generates the native SBOM/provenance/signature
// artifacts a publish call requested, in the order they should be
// pushed. layoutName is only read (for the SBOM) when includeSBOM is
// true; sign, when true, wraps the provenance predicate (if any) and
// always adds a standalone signature artifact over published's digest.
func BuildArtifacts(layoutName string, published registry.Result, includeSBOM bool, provenancePath, journalPath, builderID string, sign bool, keyDir, keyName string) ([]Artifact, error) {
	return buildArtifacts(layoutName, published, includeSBOM, provenancePath, journalPath, builderID, sign, keyDir, keyName, nil)
}

// BuildArtifactsWithAttestations adds a validated collection of external
// predicates. Platform Factory injects the actual published subject and signs
// every statement; callers cannot smuggle a different subject digest.
func BuildArtifactsWithAttestations(layoutName string, published registry.Result, includeSBOM bool, provenancePath, journalPath, builderID string, sign bool, keyDir, keyName string, externalAttestations []string) ([]Artifact, error) {
	return buildArtifacts(layoutName, published, includeSBOM, provenancePath, journalPath, builderID, sign, keyDir, keyName, externalAttestations)
}

// ValidateExternalAttestations performs the same strict, side-effect-free
// input validation used during artifact construction. The CLI calls it before
// uploading the subject manifest, then construction revalidates to close the
// mutation window before a tag can move.
func ValidateExternalAttestations(paths []string) error {
	for _, path := range paths {
		if _, err := loadExternalAttestation(path); err != nil {
			return fmt.Errorf("external attestation %s: %w", path, err)
		}
	}
	return nil
}

func buildArtifacts(layoutName string, published registry.Result, includeSBOM bool, provenancePath, journalPath, builderID string, sign bool, keyDir, keyName string, externalAttestations []string) ([]Artifact, error) {
	if len(externalAttestations) > 0 && !sign {
		return nil, errors.New("external attestations require --sign so untrusted predicates are never published unsigned")
	}
	if len(externalAttestations) > 0 {
		hexDigest := strings.TrimPrefix(published.Digest, "sha256:")
		decoded, err := hex.DecodeString(hexDigest)
		if !strings.HasPrefix(published.Digest, "sha256:") || len(decoded) != 32 || err != nil {
			return nil, errors.New("external attestations require a valid sha256 published subject digest")
		}
	}
	var store signing.KeyStore
	var keyID string
	if sign {
		if keyDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			keyDir = filepath.Join(home, ".platform-factory", "keys")
		}
		fileStore, err := signing.NewFileKeyStore(keyDir)
		if err != nil {
			return nil, err
		}
		store = fileStore
		publicKey, err := store.PublicKey(keyName)
		if err != nil {
			return nil, err
		}
		keyID = "ed25519:" + base64.RawURLEncoding.EncodeToString(publicKey)
	}
	var artifacts []Artifact
	if includeSBOM {
		sbomService := sbom.New()
		paths, err := sbomService.CollectPaths([]string{layoutName})
		if err != nil {
			return nil, err
		}
		document, err := sbomService.Generate(paths)
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(document)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, Artifact{
			Name: "SBOM", ArtifactType: "application/vnd.platform-factory.sbom.v1+json",
			PayloadType: "application/json", Payload: payload,
		})
	}
	if provenancePath != "" || journalPath != "" {
		var payload []byte
		var err error
		if journalPath != "" {
			file, openErr := os.Open(journalPath)
			if openErr != nil {
				return nil, openErr
			}
			predicate, generateErr := provenancegen.FromJournal(file, builderID)
			_ = file.Close()
			if generateErr != nil {
				return nil, generateErr
			}
			payload, err = json.Marshal(predicate)
		} else {
			payload, err = os.ReadFile(provenancePath)
		}
		if err != nil {
			return nil, err
		}
		if !json.Valid(payload) {
			return nil, errors.New("provenance predicate must be valid JSON")
		}
		if sign {
			var predicate any
			if err := json.Unmarshal(payload, &predicate); err != nil {
				return nil, err
			}
			envelope, err := attestation.Sign(store, keyName, keyID,
				"application/vnd.in-toto+json", predicate)
			if err != nil {
				return nil, err
			}
			payload, err = json.Marshal(envelope)
			if err != nil {
				return nil, err
			}
		}
		artifacts = append(artifacts, Artifact{
			Name: "provenance", ArtifactType: "application/vnd.platform-factory.provenance.v1+json",
			PayloadType: "application/json", Payload: payload,
		})
	}
	if sign {
		envelope, err := attestation.Sign(store, keyName, keyID,
			"application/vnd.platform-factory.subject.v1+json",
			map[string]string{"digest": published.Digest, "reference": published.Reference})
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(envelope)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, Artifact{
			Name: "signature", ArtifactType: "application/vnd.platform-factory.signature.v1+json",
			PayloadType: attestation.EnvelopeMediaType, Payload: payload,
		})
	}
	for _, path := range externalAttestations {
		input, err := loadExternalAttestation(path)
		if err != nil {
			return nil, fmt.Errorf("external attestation %s: %w", path, err)
		}
		var predicate any
		if err := json.Unmarshal(input.Predicate, &predicate); err != nil {
			return nil, fmt.Errorf("external attestation %s predicate: %w", input.Name, err)
		}
		digest := strings.TrimPrefix(published.Digest, "sha256:")
		statement := map[string]any{
			"_type":         "https://in-toto.io/Statement/v1",
			"subject":       []any{map[string]any{"name": published.Reference, "digest": map[string]string{"sha256": digest}}},
			"predicateType": input.PredicateType,
			"predicate":     predicate,
		}
		envelope, err := attestation.Sign(store, keyName, keyID, "application/vnd.in-toto+json", statement)
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(envelope)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, Artifact{
			Name: "attestation " + input.Name, ArtifactType: "application/vnd.platform-factory.attestation.v1+json",
			PayloadType: attestation.EnvelopeMediaType, Payload: payload,
		})
	}
	return artifacts, nil
}

type externalAttestation struct {
	APIVersion    string          `json:"api_version"`
	Name          string          `json:"name"`
	PredicateType string          `json:"predicate_type"`
	Predicate     json.RawMessage `json:"predicate"`
}

func loadExternalAttestation(path string) (externalAttestation, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return externalAttestation{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 1<<20 {
		return externalAttestation{}, errors.New("file must be regular, non-symlink, non-empty, and at most 1 MiB")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return externalAttestation{}, err
	}
	var input externalAttestation
	if err := strictjson.Decode(raw, &input); err != nil {
		return externalAttestation{}, err
	}
	if input.APIVersion != "platform-factory.dev/external-attestation/v1" {
		return externalAttestation{}, errors.New("unsupported api_version")
	}
	if input.Name == "" || len(input.Name) > 128 || strings.ContainsAny(input.Name, "\x00\r\n") {
		return externalAttestation{}, errors.New("name must be non-empty, NUL/newline-free, and at most 128 bytes")
	}
	if !strings.HasPrefix(input.PredicateType, "https://") || len(input.PredicateType) > 512 {
		return externalAttestation{}, errors.New("predicate_type must be a bounded https URI")
	}
	var predicate map[string]any
	if len(input.Predicate) == 0 || json.Unmarshal(input.Predicate, &predicate) != nil || predicate == nil {
		return externalAttestation{}, errors.New("predicate must be a JSON object")
	}
	return input, nil
}

// TransitionWorkload records publish progress without changing the
// registry operation's outcome when state persistence fails.
func TransitionWorkload(store workloadstate.Store, workloadID core.WorkloadID, to core.Phase) (warning string, ok bool) {
	if store == nil {
		return "", true
	}
	current, found, err := store.Lookup(workloadID)
	if err != nil {
		return fmt.Sprintf("workload state: lookup %s: %v", workloadID, err), false
	}
	if !found {
		current = core.RuntimeState{Phase: core.PhaseBuilt}
	}
	next, err := current.TransitionTo(to)
	if err != nil {
		return fmt.Sprintf("workload state: %v", err), false
	}
	if err := store.Save(workloadID, next); err != nil {
		return fmt.Sprintf("workload state: save %s: %v", workloadID, err), false
	}
	return "", true
}

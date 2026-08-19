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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/CYPT71/platform-factory/internal/app/sbom"
	"github.com/CYPT71/platform-factory/internal/attestation"
	"github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/policy"
	provenancegen "github.com/CYPT71/platform-factory/internal/provenance"
	"github.com/CYPT71/platform-factory/internal/registry"
	"github.com/CYPT71/platform-factory/internal/signing"
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
	return artifacts, nil
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

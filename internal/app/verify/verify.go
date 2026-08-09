// Package verify is the application-layer service behind
// `pf verify-release` - Sanetizer-todo.md item 8, the same extraction
// internal/app/doctor and internal/app/sbom already did for `pf doctor`
// and `pf sbom`. cmd/platform-factory/verify_release.go now only parses
// flags, calls Service.Verify, formats the result, and maps the outcome
// to an exit code; every actual verification step - layout, signature,
// provenance, SBOM, policy - lives here, testable without the CLI.
package verify

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/CYPT71/secure-oci-base/internal/attestation"
	"github.com/CYPT71/secure-oci-base/internal/layout"
	"github.com/CYPT71/secure-oci-base/internal/policy"
	"github.com/CYPT71/secure-oci-base/internal/sbom"
	"github.com/CYPT71/secure-oci-base/internal/signing"
)

// ErrInvalidArguments distinguishes a caller mistake (ambiguous or
// unknown --source-ref) from an operational verification failure - the
// CLI maps errors.Is(err, ErrInvalidArguments) to exit code 2, anything
// else from Verify to exit code 1, matching the exit codes
// runVerifyRelease returned before this extraction.
var ErrInvalidArguments = errors.New("invalid arguments")

// Decision is re-exported so callers never need to import internal/policy
// directly just to read a VerificationResult.
type Decision = policy.Decision

// VerificationResult is every piece of evidence Verify checked and
// whether it held up - a production `platform-factory publish` run's
// signature, provenance, SBOM, and policy decision, re-verified against
// artifacts already staged locally.
type VerificationResult struct {
	Path             string    `json:"path"`
	Digest           string    `json:"digest"`
	Reference        string    `json:"reference,omitempty"`
	Valid            bool      `json:"valid"`
	LayoutValid      bool      `json:"layout_valid"`
	SignatureValid   bool      `json:"signature_valid,omitempty"`
	SignatureError   string    `json:"signature_error,omitempty"`
	ProvenanceValid  bool      `json:"provenance_valid,omitempty"`
	ProvenanceSigned bool      `json:"provenance_signed,omitempty"`
	ProvenanceError  string    `json:"provenance_error,omitempty"`
	SBOMValid        bool      `json:"sbom_valid,omitempty"`
	SBOMError        string    `json:"sbom_error,omitempty"`
	PolicyDecision   *Decision `json:"policy_decision,omitempty"`
	PolicyError      string    `json:"policy_error,omitempty"`
}

// VerifyOptions is every input Verify needs - the layout to check plus
// which optional evidence files to verify against it. An empty file
// path skips that step entirely (matching runVerifyRelease's original
// "only verify what was given" contract).
type VerifyOptions struct {
	LayoutPath      string
	SourceReference string
	SignatureFile   string
	ProvenanceFile  string
	SBOMFile        string
	PolicyFile      string
	EvidenceFile    string
	TrustedKeyFlags []string
	KeyDir          string
	KeyName         string
}

// Service holds the two real I/O dependencies Verify needs, both
// injectable so tests never touch a real filesystem or construct a real
// key store.
type Service struct {
	// VerifyLayout validates an OCI layout - normally layout.Verify.
	VerifyLayout func(path string) (layout.Report, error)
	// LoadKeyStoreKey loads one named key from a signing.FileKeyStore
	// directory - normally backed by signing.NewFileKeyStore.
	LoadKeyStoreKey func(dir, name string) (ed25519.PublicKey, error)
}

// New returns a Service wired to the real layout verifier and key store.
func New() Service {
	return Service{
		VerifyLayout: layout.Verify,
		LoadKeyStoreKey: func(dir, name string) (ed25519.PublicKey, error) {
			store, err := signing.NewFileKeyStore(dir)
			if err != nil {
				return nil, err
			}
			return store.PublicKey(name)
		},
	}
}

// Verify performs every verification step opts requests, in order:
// layout, then (if the corresponding file was given) signature,
// provenance, SBOM, and policy. A non-nil error means verification
// could not proceed at all (bad layout, ambiguous platform selection,
// unloadable trusted keys) - VerificationResult is only meaningful when
// err is nil. Within a successful call, VerificationResult.Valid is
// false if any individual step failed; each step's own *Error field
// carries why.
func (s Service) Verify(opts VerifyOptions) (VerificationResult, error) {
	report, err := s.VerifyLayout(opts.LayoutPath)
	if err != nil {
		return VerificationResult{}, fmt.Errorf("verify layout: %w", err)
	}
	if !report.Valid {
		return VerificationResult{}, errors.New("layout reported invalid")
	}
	digest, reference, err := SelectPlatform(report, opts.SourceReference)
	if err != nil {
		return VerificationResult{}, fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}

	result := VerificationResult{Path: opts.LayoutPath, Digest: digest, Reference: reference, LayoutValid: true}

	trustedKeys, err := s.LoadTrustedKeys(opts.TrustedKeyFlags, opts.KeyDir, opts.KeyName)
	if err != nil {
		return VerificationResult{}, err
	}

	ok := true
	if opts.SignatureFile != "" {
		if err := s.VerifySignature(opts.SignatureFile, digest, trustedKeys); err != nil {
			result.SignatureError = err.Error()
			ok = false
		} else {
			result.SignatureValid = true
		}
	}
	if opts.ProvenanceFile != "" {
		signed, err := s.VerifyProvenance(opts.ProvenanceFile, trustedKeys)
		if err != nil {
			result.ProvenanceError = err.Error()
			ok = false
		} else {
			result.ProvenanceValid = true
			result.ProvenanceSigned = signed
		}
	}
	if opts.SBOMFile != "" {
		if err := VerifySBOM(opts.SBOMFile); err != nil {
			result.SBOMError = err.Error()
			ok = false
		} else {
			result.SBOMValid = true
		}
	}
	if opts.PolicyFile != "" {
		decision, err := EvaluatePolicy(opts.PolicyFile, opts.EvidenceFile, digest,
			opts.SBOMFile != "" && result.SBOMValid, opts.ProvenanceFile != "" && result.ProvenanceValid,
			opts.SignatureFile != "" && result.SignatureValid)
		if err != nil {
			result.PolicyError = err.Error()
			ok = false
		} else {
			result.PolicyDecision = &decision
			if !decision.Allowed {
				ok = false
			}
		}
	}
	result.Valid = ok
	return result, nil
}

// SelectPlatform mirrors runPublish's own single-vs-multi-platform
// reference selection so the same --source-ref value used to publish
// also selects the right platform digest here. It has no I/O, so it
// needs no Service receiver.
func SelectPlatform(report layout.Report, sourceReference string) (digest, reference string, err error) {
	references := map[string]string{}
	for _, platform := range report.Platforms {
		if platform.Reference != "" {
			references[platform.Reference] = platform.Digest
		}
	}
	if sourceReference != "" {
		digest, ok := references[sourceReference]
		if !ok {
			return "", "", fmt.Errorf("source reference %q not found in layout", sourceReference)
		}
		return digest, sourceReference, nil
	}
	if len(references) == 1 {
		for reference, digest := range references {
			return digest, reference, nil
		}
	}
	if len(report.Platforms) == 1 {
		return report.Platforms[0].Digest, report.Platforms[0].Reference, nil
	}
	return "", "", errors.New("layout contains multiple image references; select one with --source-ref")
}

// LoadTrustedKeys resolves every --trusted-key flag plus, if keyDir is
// set, one more key loaded from a signing.FileKeyStore - the complete
// pinned trust set Verify checks signatures and signed provenance
// against. Trust is never inferred from an envelope's own claimed key
// ID (see the package doc comment's threat-model note).
func (s Service) LoadTrustedKeys(flagged []string, keyDir, keyName string) (map[string]ed25519.PublicKey, error) {
	keys := map[string]ed25519.PublicKey{}
	for _, raw := range flagged {
		algo, encoded, found := strings.Cut(raw, ":")
		if !found || algo != "ed25519" {
			return nil, fmt.Errorf("--trusted-key %q must have the form ed25519:BASE64URL", raw)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("--trusted-key %q is not a valid ed25519 public key", raw)
		}
		keys[raw] = ed25519.PublicKey(decoded)
	}
	if keyDir != "" {
		publicKey, err := s.LoadKeyStoreKey(keyDir, keyName)
		if err != nil {
			return nil, err
		}
		keyID := "ed25519:" + base64.RawURLEncoding.EncodeToString(publicKey)
		keys[keyID] = publicKey
	}
	return keys, nil
}

// VerifySignature checks path's DSSE envelope against trustedKeys and
// confirms the signed subject's digest matches wantDigest.
func (s Service) VerifySignature(path, wantDigest string, trustedKeys map[string]ed25519.PublicKey) error {
	if len(trustedKeys) == 0 {
		return errors.New("no trusted key pinned: pass --trusted-key or --key-dir/--key-name")
	}
	envelope, err := readEnvelope(path)
	if err != nil {
		return err
	}
	payload, err := attestation.Verify(envelope, trustedKeys)
	if err != nil {
		return err
	}
	var subject struct {
		Digest    string `json:"digest"`
		Reference string `json:"reference"`
	}
	if err := json.Unmarshal(payload, &subject); err != nil {
		return fmt.Errorf("decode signed subject: %w", err)
	}
	if subject.Digest != wantDigest {
		return fmt.Errorf("signature covers digest %q, layout resolved to %q", subject.Digest, wantDigest)
	}
	return nil
}

// VerifyProvenance accepts either a signed DSSE envelope (checked
// against trustedKeys) or a raw provenance predicate (only validated as
// well-formed JSON), matching runPublish's own --provenance contract
// where signing provenance is controlled by --sign, not mandatory.
func (s Service) VerifyProvenance(path string, trustedKeys map[string]ed25519.PublicKey) (signed bool, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var envelope attestation.Envelope
	if json.Unmarshal(raw, &envelope) == nil && envelope.PayloadType != "" && len(envelope.Signatures) > 0 {
		if len(trustedKeys) == 0 {
			return false, errors.New("provenance is signed but no trusted key pinned: pass --trusted-key or --key-dir/--key-name")
		}
		if _, err := attestation.Verify(envelope, trustedKeys); err != nil {
			return false, err
		}
		return true, nil
	}
	if !json.Valid(raw) {
		return false, errors.New("provenance predicate must be valid JSON")
	}
	return false, nil
}

// VerifySBOM confirms path decodes as exactly one well-formed
// sbom.Document. It has no injectable dependency (a plain os.ReadFile
// equivalent) since there is nothing meaningful to fake here beyond
// what the standard library's own os package already lets a test
// exercise via a real temp file.
func VerifySBOM(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var document sbom.Document
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode SBOM: %w", err)
	}
	if decoder.Decode(new(struct{})) != io.EOF {
		return errors.New("SBOM file must contain exactly one JSON object")
	}
	return nil
}

// EvaluatePolicy decodes policyPath and evidencePath and evaluates the
// publication policy exactly as runPublish would have, filling in
// SubjectDigest/SBOM/Provenance/Signature from what Verify already
// confirmed rather than trusting the evidence file's own claims about
// them.
func EvaluatePolicy(policyPath, evidencePath, digest string, hasSBOM, hasProvenance, hasSignature bool) (policy.Decision, error) {
	if evidencePath == "" {
		return policy.Decision{}, errors.New("--evidence is required with --policy")
	}
	decode := func(path string, target any) error {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(target); err != nil {
			return err
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return errors.New("file must contain exactly one JSON object")
		}
		return nil
	}
	var rules policy.Rules
	if err := decode(policyPath, &rules); err != nil {
		return policy.Decision{}, fmt.Errorf("decode rules: %w", err)
	}
	var evidence policy.Evidence
	if err := decode(evidencePath, &evidence); err != nil {
		return policy.Decision{}, fmt.Errorf("decode evidence: %w", err)
	}
	evidence.SubjectDigest = digest
	evidence.SBOM = hasSBOM
	evidence.Provenance = hasProvenance
	evidence.Signature = hasSignature
	return policy.Evaluate(rules, evidence)
}

func readEnvelope(path string) (attestation.Envelope, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return attestation.Envelope{}, err
	}
	var envelope attestation.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return attestation.Envelope{}, fmt.Errorf("decode signature envelope: %w", err)
	}
	return envelope, nil
}

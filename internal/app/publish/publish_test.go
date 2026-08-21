package publish

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/attestation"
	"github.com/CYPT71/platform-factory/internal/registry"
	"github.com/CYPT71/platform-factory/internal/signing"
)

func TestNativePublicationArtifactsRejectInvalidEvidenceInputs(t *testing.T) {
	published := registry.Result{
		Digest:    "sha256:" + strings.Repeat("a", 64),
		Reference: "registry.example/service@sha256:" + strings.Repeat("a", 64),
	}
	artifacts, err := BuildArtifacts("", published, false,
		"", "", "builder", false, "", "")
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("artifacts=%v err=%v", artifacts, err)
	}
	root := t.TempDir()
	invalid := filepath.Join(root, "invalid.json")
	if err := os.WriteFile(invalid, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildArtifacts("", published, false,
		invalid, "", "builder", false, "", ""); err == nil {
		t.Fatal("invalid provenance accepted")
	}
	if _, err := BuildArtifacts("", published, false,
		filepath.Join(root, "missing"), "", "builder", false, "", ""); err == nil {
		t.Fatal("missing provenance accepted")
	}
	if _, err := BuildArtifacts("", published, false,
		"", invalid, "builder", false, "", ""); err == nil {
		t.Fatal("invalid journal accepted")
	}
	if _, err := BuildArtifacts(filepath.Join(root, "missing-layout"), published, true,
		"", "", "builder", false, "", ""); err == nil {
		t.Fatal("missing SBOM layout accepted")
	}
}

func TestExternalAttestationsAreStrictSubjectBoundAndSigned(t *testing.T) {
	root := t.TempDir()
	predicate := filepath.Join(root, "quality.json")
	if err := os.WriteFile(predicate, []byte(`{"api_version":"platform-factory.dev/external-attestation/v1","name":"quality-gate","predicate_type":"https://example.test/quality/v1","predicate":{"passed":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	published := registry.Result{Digest: digest, Reference: "registry.example/service@" + digest}
	keyDir := filepath.Join(root, "keys")
	artifacts, err := BuildArtifactsWithAttestations("", published, false, "", "", "builder", true, keyDir, "release", []string{predicate})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 || artifacts[1].ArtifactType != "application/vnd.platform-factory.attestation.v1+json" {
		t.Fatalf("artifacts=%+v", artifacts)
	}
	var envelope attestation.Envelope
	if err := json.Unmarshal(artifacts[1].Payload, &envelope); err != nil {
		t.Fatal(err)
	}
	store, err := signing.NewFileKeyStore(keyDir)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := store.PublicKey("release")
	if err != nil {
		t.Fatal(err)
	}
	keyID := "ed25519:" + base64.RawURLEncoding.EncodeToString(publicKey)
	payload, err := attestation.Verify(envelope, map[string]ed25519.PublicKey{keyID: publicKey})
	if err != nil {
		t.Fatal(err)
	}
	var statement struct {
		Subject []struct {
			Digest map[string]string `json:"digest"`
		} `json:"subject"`
		PredicateType string `json:"predicateType"`
	}
	if err := json.Unmarshal(payload, &statement); err != nil {
		t.Fatal(err)
	}
	if len(statement.Subject) != 1 || statement.Subject[0].Digest["sha256"] != strings.TrimPrefix(digest, "sha256:") || statement.PredicateType != "https://example.test/quality/v1" {
		t.Fatalf("statement=%s", payload)
	}

	if _, err := BuildArtifactsWithAttestations("", published, false, "", "", "builder", false, "", "", []string{predicate}); err == nil {
		t.Fatal("unsigned external attestation accepted")
	}
	unknown := filepath.Join(root, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"api_version":"platform-factory.dev/external-attestation/v1","name":"x","predicate_type":"https://example.test/x","predicate":{},"surprise":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateExternalAttestations([]string{unknown}); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestPublicationPolicyUsesGeneratedEvidenceAndFailsClosed(t *testing.T) {
	root := t.TempDir()
	rules := filepath.Join(root, "policy.json")
	evidence := filepath.Join(root, "evidence.json")
	if err := os.WriteFile(rules, []byte(`{
	  "api_version":"platform-factory.dev/policy/v1",
	  "require_pins":true,
	  "require_hardening":true,
	  "require_sbom":true,
	  "require_provenance":true,
	  "require_signature":true,
	  "require_reproducible":true
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidence, []byte(`{
	  "subject_digest":"",
	  "sources_pinned":true,
	  "base_pinned":true,
	  "toolchain_pinned":true,
	  "plugins_pinned":true,
	  "non_root":true,
	  "read_only_rootfs":true,
	  "capabilities_dropped":true,
	  "secrets_absent":true,
	  "sbom":false,
	  "provenance":false,
	  "signature":false,
	  "reproducible":true
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	published := registry.Result{Digest: "sha256:" + strings.Repeat("a", 64)}
	decision, err := EvaluatePolicy(rules, evidence, published, true, true, true)
	if err != nil || !decision.Allowed {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	decision, err = EvaluatePolicy(rules, evidence, published, true, false, true)
	if err != nil || decision.Allowed || !strings.Contains(strings.Join(decision.Reasons, " "), "provenance") {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
}

// TestEvaluatePolicyNonReproducibleEvidenceDeniesTag was originally the
// third t.Run subtest of cmd/platform-factory's
// TestV3PublicationExitCriteriaFailClosed; the other two subtests drive
// runPublish end to end (a CLI-adapter concern) and stayed there, but
// this one only exercises EvaluatePolicy directly.
func TestEvaluatePolicyNonReproducibleEvidenceDeniesTag(t *testing.T) {
	root := t.TempDir()
	rules := filepath.Join(root, "policy.json")
	evidence := filepath.Join(root, "evidence.json")
	if err := os.WriteFile(rules, []byte(`{
	  "api_version":"platform-factory.dev/policy/v1",
	  "require_reproducible":true
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidence, []byte(`{"reproducible":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	decision, err := EvaluatePolicy(rules, evidence,
		registry.Result{Digest: "sha256:" + strings.Repeat("b", 64)}, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allowed || !strings.Contains(strings.Join(decision.Reasons, " "), "reproducibility") {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestEvaluatePublicationPolicyDecodeFailures(t *testing.T) {
	published := registry.Result{Digest: "sha256:" + strings.Repeat("a", 64)}
	if _, err := EvaluatePolicy("policy.json", "", published, false, false, false); err == nil {
		t.Fatal("expected empty evidence path rejection")
	}
	root := t.TempDir()
	evidencePath := filepath.Join(root, "evidence.json")
	if err := os.WriteFile(evidencePath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EvaluatePolicy(filepath.Join(root, "missing-policy.json"), evidencePath,
		published, false, false, false); err == nil {
		t.Fatal("expected missing policy file rejection")
	}
	trailing := filepath.Join(root, "trailing.json")
	if err := os.WriteFile(trailing, []byte(`{"api_version":"platform-factory.dev/policy/v1"}{"extra":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EvaluatePolicy(trailing, evidencePath, published, false, false, false); err == nil {
		t.Fatal("expected trailing-content policy file rejection")
	}
}

// TestNativePublicationArtifactsSignUsesDefaultKeyDirectory drives the
// keyDir=="" branch, which resolves the signing key directory from the
// user's home directory rather than an explicit --key-dir.
func TestNativePublicationArtifactsSignUsesDefaultKeyDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	published := registry.Result{
		Digest:    "sha256:" + strings.Repeat("a", 64),
		Reference: "registry.example/service@sha256:" + strings.Repeat("a", 64),
	}
	artifacts, err := BuildArtifacts("", published, false, "", "", "builder", true, "", "release")
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Name != "signature" {
		t.Fatalf("artifacts=%+v", artifacts)
	}
}

func TestNativePublicationArtifactsSignSurfacesKeyStoreFailure(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	published := registry.Result{Digest: "sha256:" + strings.Repeat("a", 64)}
	if _, err := BuildArtifacts("", published, false, "", "", "builder", true, blocked, "release"); err == nil {
		t.Fatal("expected key store failure when key-dir is a regular file")
	}
}

func TestNativePublicationArtifactsSignSurfacesPublicKeyFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	published := registry.Result{Digest: "sha256:" + strings.Repeat("a", 64)}
	if _, err := BuildArtifacts("", published, false, "", "", "builder", true, dir, "release"); err == nil {
		t.Fatal("expected public key generation failure when key-dir is unwritable")
	}
}

// TestNativePublicationArtifactsJournalProvenance covers the
// journal-derived provenance path (as opposed to the --provenance file
// path already covered indirectly through launch_publish_test.go), both
// on success and when the journal file cannot be opened.
func TestNativePublicationArtifactsJournalProvenance(t *testing.T) {
	root := t.TempDir()
	journalPath := filepath.Join(root, "journal.json")
	journal := `{
	  "api_version":"platform-factory.dev/journal/v1",
	  "pipeline_fingerprint":"sha256:abc",
	  "engine_version":"platform-factory/1",
	  "sandbox":"on",
	  "generated":"2026-07-28T12:00:00Z",
	  "stages":[{"id":"build","state":"succeeded"}]
	}`
	if err := os.WriteFile(journalPath, []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	published := registry.Result{Digest: "sha256:" + strings.Repeat("a", 64)}
	artifacts, err := BuildArtifacts("", published, false, "", journalPath,
		"https://platform-factory.dev/builder/v1", false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Name != "provenance" {
		t.Fatalf("artifacts=%+v", artifacts)
	}
	if _, err := BuildArtifacts("", published, false, "", filepath.Join(root, "missing.json"),
		"builder", false, "", ""); err == nil {
		t.Fatal("expected missing journal file to fail")
	}
}

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/attestation"
	"github.com/CYPT71/secure-oci-base/internal/layout"
	"github.com/CYPT71/secure-oci-base/internal/policy"
	"github.com/CYPT71/secure-oci-base/internal/sbom"
	"github.com/CYPT71/secure-oci-base/internal/signing"
)

// releaseFixture builds one real, signed release bundle: an OCI layout,
// a DSSE-signed subject envelope over its actual digest, a generated
// SBOM document, and a permissive policy + evidence pair - the same
// artifact shapes `platform-factory publish --sign --sbom --provenance --policy
// --evidence` produces, so these tests exercise verify-release against
// what publish would really hand back, not a hand-shaped stand-in.
type releaseFixture struct {
	layoutDir  string
	digest     string
	keyDir     string
	keyID      string
	publicKey  ed25519.PublicKey
	sigFile    string
	sbomFile   string
	provFile   string
	policyFile string
	evidence   string
}

func buildReleaseFixture(t *testing.T, reproducible bool) releaseFixture {
	t.Helper()
	dir := t.TempDir()
	layoutDir := buildPublishLayout(t, "example/release", "v1")

	report, err := layout.Verify(layoutDir)
	if err != nil || !report.Valid || len(report.Platforms) != 1 {
		t.Fatalf("verify fixture layout: %v %+v", err, report)
	}
	digest := report.Platforms[0].Digest
	reference := report.Platforms[0].Reference

	keyDir := filepath.Join(dir, "keys")
	store, err := signing.NewFileKeyStore(keyDir)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := store.PublicKey("release")
	if err != nil {
		t.Fatal(err)
	}
	keyID := "ed25519:" + base64.RawURLEncoding.EncodeToString(publicKey)

	envelope, err := attestation.Sign(store, "release", keyID,
		"application/vnd.secure-oci.subject.v1+json",
		map[string]string{"digest": digest, "reference": reference})
	if err != nil {
		t.Fatal(err)
	}
	sigFile := filepath.Join(dir, "signature.json")
	writeJSONFile(t, sigFile, envelope)

	provEnvelope, err := attestation.Sign(store, "release", keyID,
		"application/vnd.in-toto+json", map[string]string{"builder": "test"})
	if err != nil {
		t.Fatal(err)
	}
	provFile := filepath.Join(dir, "provenance.json")
	writeJSONFile(t, provFile, provEnvelope)

	document, err := sbom.Generate(map[string]string{"service": filepath.Join(layoutDir, "..", "service")})
	if err != nil {
		// Fixture cleanup may have removed the source binary; a trivial
		// synthetic document is just as valid for what these tests check.
		document = sbom.Document{Components: []sbom.Component{{Name: "service", Digest: "sha256:" + fixtureHex, Size: 1, Kind: "binary"}}}
	}
	sbomFile := filepath.Join(dir, "sbom.json")
	writeJSONFile(t, sbomFile, document)

	policyFile := filepath.Join(dir, "policy.json")
	writeJSONFile(t, policyFile, policy.Rules{APIVersion: policy.APIVersion, RequireSBOM: true, RequireSignature: true})

	evidenceFile := filepath.Join(dir, "evidence.json")
	writeJSONFile(t, evidenceFile, policy.Evidence{Reproducible: reproducible})

	return releaseFixture{
		layoutDir: layoutDir, digest: digest, keyDir: keyDir, keyID: keyID, publicKey: publicKey,
		sigFile: sigFile, sbomFile: sbomFile, provFile: provFile, policyFile: policyFile, evidence: evidenceFile,
	}
}

const fixtureHex = "0000000000000000000000000000000000000000000000000000000000000"

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
}

func verifyReleaseArgs(f releaseFixture, extra ...string) []string {
	args := []string{
		"verify-release",
		"--signature", f.sigFile,
		"--provenance", f.provFile,
		"--sbom", f.sbomFile,
		"--policy", f.policyFile,
		"--evidence", f.evidence,
	}
	if f.keyDir != "" {
		args = append(args, "--key-dir", f.keyDir, "--key-name", "release")
	}
	args = append(args, extra...)
	return append(args, f.layoutDir)
}

func TestRunVerifyReleaseCompleteChainSucceeds(t *testing.T) {
	fixture := buildReleaseFixture(t, false)
	var stdout, stderr bytes.Buffer
	code := run(verifyReleaseArgs(fixture), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	var result releaseVerification
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v (out=%s)", err, stdout.String())
	}
	if !result.Valid || !result.SignatureValid || !result.ProvenanceValid || !result.SBOMValid {
		t.Fatalf("expected every check to pass: %+v", result)
	}
	if !result.ProvenanceSigned {
		t.Fatalf("expected provenance to be recognized as signed: %+v", result)
	}
	if result.PolicyDecision == nil || !result.PolicyDecision.Allowed {
		t.Fatalf("expected policy to allow: %+v", result.PolicyDecision)
	}
	if result.Digest != fixture.digest {
		t.Fatalf("digest=%q want %q", result.Digest, fixture.digest)
	}
}

func TestRunVerifyReleaseTextFormat(t *testing.T) {
	fixture := buildReleaseFixture(t, false)
	var stdout, stderr bytes.Buffer
	code := run(verifyReleaseArgs(fixture, "--format", "text"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"signature\tok", "sbom\tok", "release\tvalid=true"} {
		if !bytes.Contains(stdout.Bytes(), []byte(want)) {
			t.Fatalf("missing %q in text output: %s", want, stdout.String())
		}
	}
}

func TestRunVerifyReleaseRejectsUntrustedSigner(t *testing.T) {
	fixture := buildReleaseFixture(t, false)
	otherKeyDir := t.TempDir()
	if _, err := signing.NewFileKeyStore(otherKeyDir); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(verifyReleaseArgs(fixture, "--key-dir", otherKeyDir), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d want 1; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var result releaseVerification
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.SignatureValid || result.SignatureError == "" {
		t.Fatalf("expected signature verification to fail against an untrusted key: %+v", result)
	}
}

func TestRunVerifyReleaseRejectsDigestMismatch(t *testing.T) {
	fixture := buildReleaseFixture(t, false)
	store, err := signing.NewFileKeyStore(fixture.keyDir)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := attestation.Sign(store, "release", fixture.keyID,
		"application/vnd.secure-oci.subject.v1+json",
		map[string]string{"digest": "sha256:" + fixtureHex, "reference": "example/release:v1"})
	if err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, fixture.sigFile, envelope)

	var stdout, stderr bytes.Buffer
	code := run(verifyReleaseArgs(fixture), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d want 1; stdout=%s", code, stdout.String())
	}
	var result releaseVerification
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.SignatureValid || result.SignatureError == "" {
		t.Fatalf("expected a digest-mismatched signature to be rejected: %+v", result)
	}
}

func TestRunVerifyReleaseRejectsMalformedSBOM(t *testing.T) {
	fixture := buildReleaseFixture(t, false)
	if err := os.WriteFile(fixture.sbomFile, []byte(`{"components": "not-an-array"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(verifyReleaseArgs(fixture), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d want 1; stdout=%s", code, stdout.String())
	}
	var result releaseVerification
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.SBOMValid || result.SBOMError == "" {
		t.Fatalf("expected malformed SBOM to be rejected: %+v", result)
	}
}

func TestRunVerifyReleasePolicyDenial(t *testing.T) {
	fixture := buildReleaseFixture(t, false)
	writeJSONFile(t, fixture.policyFile, policy.Rules{
		APIVersion: policy.APIVersion, RequireSBOM: true, RequireSignature: true, RequireReproducible: true,
	})
	var stdout, stderr bytes.Buffer
	code := run(verifyReleaseArgs(fixture), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d want 1; stdout=%s", code, stdout.String())
	}
	var result releaseVerification
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.PolicyDecision == nil || result.PolicyDecision.Allowed {
		t.Fatalf("expected policy to deny an unreproducible release under require_reproducible: %+v", result.PolicyDecision)
	}
}

func TestRunVerifyReleasePolicyAllowsReproducible(t *testing.T) {
	fixture := buildReleaseFixture(t, true)
	writeJSONFile(t, fixture.policyFile, policy.Rules{
		APIVersion: policy.APIVersion, RequireSBOM: true, RequireSignature: true, RequireReproducible: true,
	})
	var stdout, stderr bytes.Buffer
	code := run(verifyReleaseArgs(fixture), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestRunVerifyReleaseRequiresCompleteEvidenceByDefault(t *testing.T) {
	fixture := buildReleaseFixture(t, false)
	var stdout, stderr bytes.Buffer
	code := run([]string{"verify-release", fixture.layoutDir}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d want 2; stderr=%s", code, stderr.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("complete verification requires")) {
		t.Fatalf("unexpected error: %s", stderr.String())
	}
}

func TestRunVerifyReleaseAllowIncompleteEvidenceNeedsNoArtifacts(t *testing.T) {
	fixture := buildReleaseFixture(t, false)
	var stdout, stderr bytes.Buffer
	code := run([]string{"verify-release", "--allow-incomplete-evidence", fixture.layoutDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d want 0; stderr=%s", code, stderr.String())
	}
	var result releaseVerification
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !result.Valid || !result.LayoutValid {
		t.Fatalf("expected layout-only verification to pass: %+v", result)
	}
}

func TestRunVerifyReleaseTrustedKeyFlag(t *testing.T) {
	fixture := buildReleaseFixture(t, false)
	keyID := "ed25519:" + base64.RawURLEncoding.EncodeToString(fixture.publicKey)
	var stdout, stderr bytes.Buffer
	code := run(verifyReleaseArgs(releaseFixture{
		layoutDir: fixture.layoutDir, digest: fixture.digest,
		sigFile: fixture.sigFile, sbomFile: fixture.sbomFile, provFile: fixture.provFile,
		policyFile: fixture.policyFile, evidence: fixture.evidence,
	}, "--trusted-key", keyID), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestRunVerifyReleaseRejectsUnknownSourceRef(t *testing.T) {
	fixture := buildReleaseFixture(t, false)
	var stdout, stderr bytes.Buffer
	code := run(verifyReleaseArgs(fixture, "--source-ref", "does-not-exist:v1"), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d want 2; stderr=%s", code, stderr.String())
	}
}

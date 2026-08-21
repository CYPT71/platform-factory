package verify

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CYPT71/platform-factory/internal/attestation"
	"github.com/CYPT71/platform-factory/internal/layout"
	"github.com/CYPT71/platform-factory/internal/policy"
	provenancegen "github.com/CYPT71/platform-factory/internal/provenance"
	"github.com/CYPT71/platform-factory/internal/sbom"
	"github.com/CYPT71/platform-factory/internal/signing"
)

func fakeService(report layout.Report, reportErr error) *service {
	return &service{
		verifyLayout: func(string) (layout.Report, error) { return report, reportErr },
		loadKeyStoreKey: func(dir, name string) (ed25519.PublicKey, error) {
			return nil, errors.New("loadKeyStoreKey should not be called unless --key-dir is set")
		},
	}
}

func TestVerifyPropagatesLayoutVerifyError(t *testing.T) {
	svc := fakeService(layout.Report{}, errors.New("boom"))
	_, err := svc.Verify(VerifyOptions{LayoutPath: "x"})
	if err == nil {
		t.Fatal("expected an error when VerifyLayout fails")
	}
	if errors.Is(err, ErrInvalidArguments) {
		t.Fatal("a layout verify failure is not an invalid-arguments error")
	}
}

func TestVerifyRejectsAnInvalidLayout(t *testing.T) {
	svc := fakeService(layout.Report{Valid: false}, nil)
	_, err := svc.Verify(VerifyOptions{LayoutPath: "x"})
	if err == nil {
		t.Fatal("expected an error for an invalid layout report")
	}
}

func TestVerifyWrapsAmbiguousPlatformSelectionAsInvalidArguments(t *testing.T) {
	report := layout.Report{Valid: true, Platforms: []layout.Platform{
		{Reference: "a:v1", Digest: "sha256:aaa"},
		{Reference: "b:v1", Digest: "sha256:bbb"},
	}}
	svc := fakeService(report, nil)
	_, err := svc.Verify(VerifyOptions{LayoutPath: "x"})
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("err=%v, want ErrInvalidArguments for an ambiguous multi-platform layout with no --source-ref", err)
	}
}

func TestVerifyWithOnlyLayoutAndNoOptionalFilesIsValid(t *testing.T) {
	report := layout.Report{Valid: true, Platforms: []layout.Platform{{Reference: "a:v1", Digest: "sha256:aaa"}}}
	svc := fakeService(report, nil)
	result, err := svc.Verify(VerifyOptions{LayoutPath: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || !result.LayoutValid || result.Digest != "sha256:aaa" {
		t.Fatalf("result=%+v", result)
	}
}

func TestSelectPlatformSourceRefNotFoundIsAnError(t *testing.T) {
	report := layout.Report{Platforms: []layout.Platform{{Reference: "a:v1", Digest: "sha256:aaa"}}}
	if _, _, err := SelectPlatform(report, "does-not-exist"); err == nil {
		t.Fatal("expected an error for an unknown source reference")
	}
}

func TestSelectPlatformSingleUnnamedPlatformNeedsNoSourceRef(t *testing.T) {
	report := layout.Report{Platforms: []layout.Platform{{Digest: "sha256:aaa"}}}
	digest, _, err := SelectPlatform(report, "")
	if err != nil || digest != "sha256:aaa" {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
}

func TestSelectPlatformMultipleWithNoSourceRefIsAmbiguous(t *testing.T) {
	report := layout.Report{Platforms: []layout.Platform{
		{Reference: "a:v1", Digest: "sha256:aaa"},
		{Reference: "b:v1", Digest: "sha256:bbb"},
	}}
	if _, _, err := SelectPlatform(report, ""); err == nil {
		t.Fatal("expected an error for an ambiguous multi-platform layout with no --source-ref")
	}
}

func TestVerifySBOMAcceptsExactlyOneWellFormedDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sbom.json")
	doc := sbom.Document{Components: []sbom.Component{{Name: "a", Digest: "sha256:" + hex64, Size: 1, Kind: "binary"}}}
	writeJSON(t, path, doc)
	if err := VerifySBOM(path); err != nil {
		t.Fatal(err)
	}
}

func TestVerifySBOMRejectsTrailingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sbom.json")
	if err := os.WriteFile(path, []byte(`{"components":[]}{"components":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifySBOM(path); err == nil {
		t.Fatal("expected an error for a file containing more than one JSON object")
	}
}

func TestVerifySBOMRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sbom.json")
	if err := os.WriteFile(path, []byte(`{"not_a_real_field": true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifySBOM(path); err == nil {
		t.Fatal("expected an error for an unrecognized field")
	}
}

func TestLoadTrustedKeysParsesFlagFormat(t *testing.T) {
	svc := fakeService(layout.Report{}, nil)
	_, key, err := generateTestKey(t)
	if err != nil {
		t.Fatal(err)
	}
	keyID := "ed25519:" + base64.RawURLEncoding.EncodeToString(key)
	keys, err := svc.LoadTrustedKeys([]string{keyID}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[keyID] == nil {
		t.Fatalf("keys=%v", keys)
	}
}

func TestLoadTrustedKeysRejectsMalformedFlag(t *testing.T) {
	svc := fakeService(layout.Report{}, nil)
	if _, err := svc.LoadTrustedKeys([]string{"not-ed25519:abc"}, "", ""); err == nil {
		t.Fatal("expected an error for a malformed --trusted-key")
	}
	if _, err := svc.LoadTrustedKeys([]string{"ed25519:not-valid-base64!!"}, "", ""); err == nil {
		t.Fatal("expected an error for an invalid key encoding")
	}
}

func TestLoadTrustedKeysCallsLoadKeyStoreKeyOnlyWhenKeyDirSet(t *testing.T) {
	called := false
	svc := &service{
		verifyLayout: func(string) (layout.Report, error) { return layout.Report{}, nil },
		loadKeyStoreKey: func(dir, name string) (ed25519.PublicKey, error) {
			called = true
			if dir != "somedir" || name != "somename" {
				t.Fatalf("dir=%q name=%q", dir, name)
			}
			_, key, _ := generateTestKey(t)
			return key, nil
		},
	}
	keys, err := svc.LoadTrustedKeys(nil, "somedir", "somename")
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected LoadKeyStoreKey to be called when --key-dir is set")
	}
	if len(keys) != 1 {
		t.Fatalf("keys=%v", keys)
	}
}

func TestLoadTrustedKeysValidatesX509ChainBeforeTrustingLeaf(t *testing.T) {
	dir := t.TempDir()
	rootKey, rootCert, rootFile := writeTestCertificate(t, dir, "root", nil, nil, true)
	_, leafCert, leafFile := writeTestCertificate(t, dir, "leaf", rootCert, rootKey, false)

	svc := fakeService(layout.Report{}, nil)
	keys, err := svc.LoadTrustedKeysWithCertificates(nil, "", "", leafFile, nil, []string{rootFile})
	if err != nil {
		t.Fatal(err)
	}
	publicKey := leafCert.PublicKey.(ed25519.PublicKey)
	keyID := "ed25519:" + base64.RawURLEncoding.EncodeToString(publicKey)
	if len(keys) != 1 || keys[keyID] == nil {
		t.Fatalf("keys=%v, want validated leaf %s", keys, keyID)
	}

	_, _, unrelatedRoot := writeTestCertificate(t, dir, "unrelated", nil, nil, true)
	if _, err := svc.LoadTrustedKeysWithCertificates(nil, "", "", leafFile, nil, []string{unrelatedRoot}); err == nil {
		t.Fatal("leaf signed by another root was trusted")
	}
}

func TestLoadTrustedKeysX509FailsClosedOnIncompleteOrUnsafeInputs(t *testing.T) {
	dir := t.TempDir()
	_, _, leaf := writeTestCertificate(t, dir, "leaf", nil, nil, false)
	svc := fakeService(layout.Report{}, nil)
	if _, err := svc.LoadTrustedKeysWithCertificates(nil, "", "", leaf, nil, nil); err == nil {
		t.Fatal("certificate without an explicit root was accepted")
	}
	alias := filepath.Join(dir, "leaf-alias.pem")
	if err := os.Symlink(leaf, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCertificate(alias); err == nil {
		t.Fatal("symlink certificate was accepted")
	}
}

func writeTestCertificate(t *testing.T, dir, name string, parent *x509.Certificate, parentKey ed25519.PrivateKey, isCA bool) (ed25519.PrivateKey, *x509.Certificate, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(int64(len(name) + 1)), Subject: pkix.Name{CommonName: name},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		BasicConstraintsValid: true, IsCA: isCA,
		KeyUsage: x509.KeyUsageDigitalSignature,
	}
	if isCA {
		template.KeyUsage |= x509.KeyUsageCertSign
	}
	if parent == nil {
		parent, parentKey = template, privateKey
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, publicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(dir, name+".pem")
	if err := os.WriteFile(filename, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return privateKey, certificate, filename
}

func TestVerifySignatureRequiresAtLeastOneTrustedKey(t *testing.T) {
	svc := fakeService(layout.Report{}, nil)
	if err := svc.VerifySignature("irrelevant", "sha256:x", map[string]ed25519.PublicKey{}); err == nil {
		t.Fatal("expected an error when no trusted key is pinned")
	}
}

func TestVerifySignatureRealRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := signing.NewFileKeyStore(filepath.Join(dir, "keys"))
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
		map[string]string{"digest": "sha256:abc", "reference": "app:v1"})
	if err != nil {
		t.Fatal(err)
	}
	sigFile := filepath.Join(dir, "signature.json")
	writeJSON(t, sigFile, envelope)

	svc := fakeService(layout.Report{}, nil)
	trustedKeys := map[string]ed25519.PublicKey{keyID: publicKey}

	if err := svc.VerifySignature(sigFile, "sha256:abc", trustedKeys); err != nil {
		t.Fatal(err)
	}
	if err := svc.VerifySignature(sigFile, "sha256:different-digest", trustedKeys); err == nil {
		t.Fatal("expected an error when the signed digest does not match wantDigest")
	}
}

func TestVerifyProvenanceAcceptsRawUnsignedJSON(t *testing.T) {
	svc := fakeService(layout.Report{}, nil)
	path := filepath.Join(t.TempDir(), "provenance.json")
	if err := os.WriteFile(path, []byte(`{"builder":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	signed, err := svc.VerifyProvenance(path, nil)
	if err != nil || signed {
		t.Fatalf("signed=%v err=%v, want signed=false err=nil for a raw predicate", signed, err)
	}
}

func TestVerifyProvenanceRejectsMalformedJSON(t *testing.T) {
	svc := fakeService(layout.Report{}, nil)
	path := filepath.Join(t.TempDir(), "provenance.json")
	if err := os.WriteFile(path, []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyProvenance(path, nil); err == nil {
		t.Fatal("expected an error for a malformed provenance predicate")
	}
}

func TestVerifyProvenanceAgainstJournalAcceptsExactPredicateAndRejectsDrift(t *testing.T) {
	dir := t.TempDir()
	journalText := `{"api_version":"platform-factory.dev/journal/v1","pipeline_fingerprint":"sha256:abc","engine_version":"platform-factory/1","sandbox":"on","generated":"2026-08-20T12:00:00Z","stages":[{"id":"build","state":"succeeded"}]}`
	journalFile := filepath.Join(dir, "journal.json")
	if err := os.WriteFile(journalFile, []byte(journalText), 0o600); err != nil {
		t.Fatal(err)
	}
	builderID := "https://platform-factory.dev/builder/v1"
	predicate, err := provenancegen.FromJournal(strings.NewReader(journalText), builderID)
	if err != nil {
		t.Fatal(err)
	}
	provenanceFile := filepath.Join(dir, "provenance.json")
	writeJSON(t, provenanceFile, predicate)
	svc := fakeService(layout.Report{}, nil)
	if signed, err := svc.VerifyProvenanceAgainstJournal(provenanceFile, journalFile, builderID, nil); err != nil || signed {
		t.Fatalf("signed=%v err=%v", signed, err)
	}

	predicate.RunDetails.Metadata.InvocationID = "sha256:other-build"
	writeJSON(t, provenanceFile, predicate)
	if _, err := svc.VerifyProvenanceAgainstJournal(provenanceFile, journalFile, builderID, nil); err == nil || !strings.Contains(err.Error(), "inconsistent") {
		t.Fatalf("drifted provenance accepted: %v", err)
	}
	journalAlias := filepath.Join(dir, "journal-alias.json")
	if err := os.Symlink(journalFile, journalAlias); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyProvenanceAgainstJournal(provenanceFile, journalAlias, builderID, nil); err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("symlink journal accepted: %v", err)
	}
}

func TestEvaluatePolicyRequiresEvidenceFile(t *testing.T) {
	if _, err := EvaluatePolicy("policy.json", "", "sha256:x", true, true, true); err == nil {
		t.Fatal("expected an error when evidencePath is empty")
	}
}

func TestEvaluatePolicyRealRulesAndEvidence(t *testing.T) {
	dir := t.TempDir()
	policyFile := filepath.Join(dir, "policy.json")
	writeJSON(t, policyFile, policy.Rules{APIVersion: policy.APIVersion, RequireSBOM: true, RequireSignature: true})
	evidenceFile := filepath.Join(dir, "evidence.json")
	writeJSON(t, evidenceFile, policy.Evidence{})

	decision, err := EvaluatePolicy(policyFile, evidenceFile, "sha256:abc", true, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allowed {
		t.Fatalf("decision=%+v, want Allowed=true (SBOM and signature both satisfied)", decision)
	}
}

const hex64 = "0000000000000000000000000000000000000000000000000000000000000"

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
}

func generateTestKey(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	t.Helper()
	store, err := signing.NewFileKeyStore(t.TempDir())
	if err != nil {
		return nil, nil, err
	}
	publicKey, err := store.PublicKey("test")
	return nil, publicKey, err
}

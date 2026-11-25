package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/CYPT71/platform-factory/internal/license"
)

func issueTestLicense(t *testing.T, keyDir string, extra ...string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "test.license.json")
	args := append([]string{
		"license", "issue",
		"--key-dir", keyDir, "--license-id", "lic-test", "--licensee", "Test Co",
		"--edition", "enterprise", "--out", out,
	}, extra...)
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("issue: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	return out
}

func TestLicenseIssueThenVerifySucceeds(t *testing.T) {
	keyDir := t.TempDir()
	path := issueTestLicense(t, keyDir, "--seats", "5", "--feature", "sso")

	var stdout, stderr bytes.Buffer
	code := run([]string{"license", "verify", "--key-dir", keyDir, path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("verify: code=%d stderr=%s", code, stderr.String())
	}
	var got license.License
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (out=%s)", err, stdout.String())
	}
	if got.LicenseID != "lic-test" || got.Licensee != "Test Co" || got.Seats != 5 || !got.HasFeature("sso") {
		t.Fatalf("unexpected license: %+v", got)
	}
}

func TestLicenseVerifyRejectsWrongTrustedKey(t *testing.T) {
	path := issueTestLicense(t, t.TempDir())
	var stdout, stderr bytes.Buffer
	code := run([]string{"license", "verify", "--trusted-key", "ed25519:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", path}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("verify against the wrong key unexpectedly succeeded: stdout=%s", stdout.String())
	}
}

func TestLicenseVerifyRejectsExpiredLicense(t *testing.T) {
	keyDir := t.TempDir()
	path := issueTestLicense(t, keyDir, "--expires-in", "1ns")

	var stdout, stderr bytes.Buffer
	code := run([]string{"license", "verify", "--key-dir", keyDir, path}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("verify accepted an expired license: stdout=%s", stdout.String())
	}
}

func TestLicenseVerifyRequiresATrustSource(t *testing.T) {
	path := issueTestLicense(t, t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"license", "verify", path}, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d, want 2 when neither --trusted-key nor --key-dir is given; stderr=%s", code, stderr.String())
	}
}

func TestLicenseShowDoesNotRequireTrust(t *testing.T) {
	path := issueTestLicense(t, t.TempDir(), "--seats", "3")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"license", "show", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("show: code=%d stderr=%s", code, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"seats": 3`)) {
		t.Fatalf("unexpected show output: %s", stdout.String())
	}
}

func TestLicenseIssueRequiresMandatoryFlags(t *testing.T) {
	for _, args := range [][]string{
		{"license", "issue"},
		{"license", "issue", "--key-dir", t.TempDir()},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Fatalf("args=%v code=%d, want 2; stderr=%s", args, code, stderr.String())
		}
	}
}

func TestLicenseRejectsUnknownAction(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"license", "revoke"}, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d, want 2 for an unknown action; stderr=%s", code, stderr.String())
	}
}

func TestLicenseRequiresAnAction(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"license"}, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d, want 2 with no action; stderr=%s", code, stderr.String())
	}
}

func TestLicenseSubcommandsRejectUnknownFlags(t *testing.T) {
	for _, args := range [][]string{
		{"license", "issue", "--does-not-exist"},
		{"license", "verify", "--does-not-exist"},
		{"license", "show", "--does-not-exist"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Fatalf("args=%v code=%d, want 2", args, code)
		}
	}
}

func TestLicenseVerifyAndShowRejectWrongArgCount(t *testing.T) {
	path := issueTestLicense(t, t.TempDir())
	for _, args := range [][]string{
		{"license", "verify", "--key-dir", t.TempDir()},
		{"license", "verify", "--key-dir", t.TempDir(), path, "extra"},
		{"license", "show"},
		{"license", "show", path, "extra"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Fatalf("args=%v code=%d, want 2; stderr=%s", args, code, stderr.String())
		}
	}
}

func TestLicenseVerifyAndShowRejectMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	for _, args := range [][]string{
		{"license", "verify", "--key-dir", t.TempDir(), missing},
		{"license", "show", missing},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 1 {
			t.Fatalf("args=%v code=%d, want 1; stderr=%s", args, code, stderr.String())
		}
	}
}

func TestLicenseVerifyAndShowRejectMalformedEnvelopeJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "malformed.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"license", "verify", "--key-dir", t.TempDir(), path},
		{"license", "show", path},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 1 {
			t.Fatalf("args=%v code=%d, want 1; stderr=%s", args, code, stderr.String())
		}
	}
}

func TestLicenseShowRejectsMalformedPayloadEncoding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad-payload.json")
	envelope := `{"payloadType":"application/vnd.platform-factory.license.v1+json","payload":"not-valid-base64!!","signatures":[]}`
	if err := os.WriteFile(path, []byte(envelope), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"license", "show", path}, &stdout, &stderr); code != 1 {
		t.Fatalf("code=%d, want 1; stderr=%s", code, stderr.String())
	}
}

func TestLicenseShowRejectsMalformedPredicateJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad-predicate.json")
	// "not json" base64-encoded, so it decodes fine but fails JSON decode.
	envelope := `{"payloadType":"application/vnd.platform-factory.license.v1+json","payload":"bm90IGpzb24=","signatures":[]}`
	if err := os.WriteFile(path, []byte(envelope), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"license", "show", path}, &stdout, &stderr); code != 1 {
		t.Fatalf("code=%d, want 1; stderr=%s", code, stderr.String())
	}
}

func TestLicenseVerifyRejectsMalformedTrustedKeyFlag(t *testing.T) {
	path := issueTestLicense(t, t.TempDir())
	for _, trustedKey := range []string{
		"not-ed25519:AAAA",
		"no-colon-at-all",
		"ed25519:not-valid-base64!!",
		"ed25519:AAAA",
	} {
		var stdout, stderr bytes.Buffer
		code := run([]string{"license", "verify", "--trusted-key", trustedKey, path}, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("trustedKey=%q code=%d, want 2; stderr=%s", trustedKey, code, stderr.String())
		}
	}
}

func TestLicenseRejectsInvalidKeyName(t *testing.T) {
	keyDir := t.TempDir()
	path := issueTestLicense(t, keyDir)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"license", "verify", "--key-dir", keyDir, "--key-name", "Not-Valid", path}, &stdout, &stderr); code != 2 {
		t.Fatalf("verify: code=%d, want 2; stderr=%s", code, stderr.String())
	}

	out := filepath.Join(t.TempDir(), "x.license.json")
	stdout.Reset()
	stderr.Reset()
	code := run([]string{
		"license", "issue", "--key-dir", keyDir, "--key-name", "Not-Valid",
		"--license-id", "lic-x", "--licensee", "Co", "--edition", "enterprise", "--out", out,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("issue: code=%d, want 1; stderr=%s", code, stderr.String())
	}
}

func TestLicenseIssueRejectsAnUnwritableKeyDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	keyDir := filepath.Join(blocker, "sub")
	out := filepath.Join(t.TempDir(), "x.license.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"license", "issue", "--key-dir", keyDir,
		"--license-id", "lic-x", "--licensee", "Co", "--edition", "enterprise", "--out", out,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d, want 1; stderr=%s", code, stderr.String())
	}
}

func TestLicenseIssueRejectsAnUnwritableOutputPath(t *testing.T) {
	keyDir := t.TempDir()
	// --out pointing at an existing directory can never be opened for
	// writing as a regular file.
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"license", "issue", "--key-dir", keyDir,
		"--license-id", "lic-x", "--licensee", "Co", "--edition", "enterprise", "--out", out,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d, want 1; stderr=%s", code, stderr.String())
	}
}

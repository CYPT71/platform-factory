package main

import (
	"bytes"
	"encoding/json"
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

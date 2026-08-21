package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadLockStrictlyLoadsCurrentSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pf.lock")
	if err := os.WriteFile(path, []byte(`{"version":1,"git_commit":"abc123"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := LoadLock(path)
	if err != nil || lock.GitCommit != "abc123" {
		t.Fatalf("lock=%+v err=%v", lock, err)
	}
}

func TestV2LockPinsCanonicalManifestAndRejectsDrift(t *testing.T) {
	dir := t.TempDir()
	manifest := []byte("version: 1\nlanguage: go\nartifact: app\n")
	digest, err := CanonicalManifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	lock := Lock{Version: 2, PlanDigest: digest}
	encoded, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "pf.yaml")
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pf.lock"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.VerifyAdjacentLock(); err != nil {
		t.Fatal(err)
	}
	// Comments and key order are not plan changes.
	reformatted := []byte("# reviewed\nartifact: app\nlanguage: go\nversion: 1\n")
	if err := os.WriteFile(manifestPath, reformatted, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loaded.VerifyAdjacentLock(); err != nil {
		t.Fatalf("semantic reformat rejected: %v", err)
	}
	if err := os.WriteFile(manifestPath, []byte("version: 1\nlanguage: go\nartifact: other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loaded.VerifyAdjacentLock(); err == nil || !strings.Contains(err.Error(), "plan and lock disagree") {
		t.Fatalf("drift err=%v", err)
	}
}

func TestV2LockRejectsMalformedAndDuplicatePins(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	for name, lock := range map[string]Lock{
		"missing plan": {Version: 2},
		"bad pin":      {Version: 2, PlanDigest: digest, Sources: []LockedInput{{Name: "source", Digest: "latest"}}},
		"duplicate":    {Version: 2, PlanDigest: digest, Toolchains: []LockedInput{{Name: "go", Digest: digest}, {Name: "go", Digest: digest}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := lock.Validate(); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestLoadLockRejectsUntrustedInput(t *testing.T) {
	for name, value := range map[string]string{
		"unknown field":   `{"version":1,"extra":true}`,
		"future version":  `{"version":3}`,
		"missing version": `{}`,
		"trailing value":  `{"version":1}{}`,
		"invalid commit":  `{"version":1,"git_commit":"bad commit"}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pf.lock")
			if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadLock(path); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "pf.lock")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", 1<<20+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLock(path); err == nil {
		t.Fatal("expected oversized lock rejection")
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(target), "pf.lock")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLock(link); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

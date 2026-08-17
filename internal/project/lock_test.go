package project

import (
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

func TestLoadLockRejectsUntrustedInput(t *testing.T) {
	for name, value := range map[string]string{
		"unknown field":   `{"version":1,"extra":true}`,
		"future version":  `{"version":2}`,
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

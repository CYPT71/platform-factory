package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/CYPT71/platform-factory/internal/app/installer"
)

func TestEnsurePFAliasNoopWhenPlatformFactoryNotInstalled(t *testing.T) {
	prefix := t.TempDir()
	if err := ensurePFAlias(prefix, "linux"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(prefix, "pf")); !os.IsNotExist(err) {
		t.Fatalf("pf alias created without platform-factory: err=%v", err)
	}
}

func TestEnsurePFAliasPointsAtPlatformFactory(t *testing.T) {
	prefix := t.TempDir()
	suffix := installer.BinSuffix(runtime.GOOS)
	if err := os.WriteFile(filepath.Join(prefix, "platform-factory"+suffix), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePFAlias(prefix, runtime.GOOS); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(prefix, "pf"+suffix)
	if runtime.GOOS == "windows" {
		data, err := os.ReadFile(alias)
		if err != nil || string(data) != "binary" {
			t.Fatalf("pf alias content=%q err=%v, want a copy of platform-factory", data, err)
		}
		return
	}
	linkTarget, err := os.Readlink(alias)
	if err != nil {
		t.Fatalf("pf is not a symlink: %v", err)
	}
	if linkTarget != "platform-factory"+suffix {
		t.Fatalf("pf symlink target=%q, want a relative %q", linkTarget, "platform-factory"+suffix)
	}
}

func TestEnsurePFAliasReplacesAStaleAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevated privileges on Windows by default")
	}
	prefix := t.TempDir()
	if err := os.WriteFile(filepath.Join(prefix, "platform-factory"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	stalePath := filepath.Join(prefix, "pf")
	if err := os.WriteFile(stalePath, []byte("leftover from a previous install"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePFAlias(prefix, "linux"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(stalePath); err != nil {
		t.Fatalf("stale pf file was not replaced by a symlink: %v", err)
	}
}

package marketplace

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestHashEntrypointHashesARegularFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "plugin.py"), []byte("print(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := hashEntrypoint(root, "plugin.py")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("print(1)\n"))
	want := "sha256:" + hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestHashEntrypointHashesADirectoryDeterministically(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "plugin")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "helper.go"), []byte("package sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A .git directory alongside real files must never affect the digest.
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := hashEntrypoint(root, "plugin")
	if err != nil {
		t.Fatal(err)
	}
	second, err := hashEntrypoint(root, "plugin")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("hashEntrypoint is not deterministic: %q != %q", first, second)
	}

	if err := os.Remove(filepath.Join(dir, ".git", "HEAD")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	withoutGit, err := hashEntrypoint(root, "plugin")
	if err != nil {
		t.Fatal(err)
	}
	if withoutGit != first {
		t.Fatalf("a .git directory changed the digest: with=%q without=%q", first, withoutGit)
	}
}

func TestHashEntrypointRejectsAMissingPath(t *testing.T) {
	if _, err := hashEntrypoint(t.TempDir(), "does-not-exist"); err == nil {
		t.Fatal("expected an error for a missing entrypoint")
	}
}

func TestHashEntrypointRejectsANonRegularNonDirectoryEntry(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := hashEntrypoint(root, "link.txt"); err == nil {
		t.Fatal("expected an error for a symlink entrypoint")
	}
}

package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomicallyReplacesWithRequestedMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, "state", []byte("new"), 0o600, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file leaked: entries=%v err=%v", entries, err)
	}
}

func TestWriteRejectsMissingDirectoryWithoutPublishing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	if err := Write(dir, "state", []byte("data"), 0o600, false); err == nil {
		t.Fatal("write to missing directory succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, "state")); !os.IsNotExist(err) {
		t.Fatalf("unexpected target: %v", err)
	}
}

func TestWriteRejectsPathTraversal(t *testing.T) {
	if err := Write(t.TempDir(), "../escape", nil, 0o600, false); err == nil {
		t.Fatal("path traversal accepted")
	}
}

package workloadstate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CYPT71/platform-factory/internal/core"
)

func TestNewFileStoreRejectsEmptyRoot(t *testing.T) {
	if _, err := NewFileStore(""); err == nil {
		t.Fatal("accepted an empty root")
	}
}

func TestNewFileStoreCreatesDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "state")
	if _, err := NewFileStore(root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		t.Fatalf("root not created: info=%v err=%v", info, err)
	}
}

func TestNewFileStoreRejectsNonDirectoryRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(root, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(root); err == nil {
		t.Fatal("accepted a regular file as root")
	}
}

func TestFileStorePersistsAcrossInstances(t *testing.T) {
	root := t.TempDir()
	first, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Save("wl-1", core.RuntimeState{Phase: core.PhasePublished}); err != nil {
		t.Fatal(err)
	}
	second, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state, ok, err := second.Lookup("wl-1")
	if err != nil || !ok || state.Phase != core.PhasePublished {
		t.Fatalf("state=%+v ok=%v err=%v", state, ok, err)
	}
}

func TestFileStoreLookupFailsClosedOnCorruptRecord(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "wl-1"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Lookup("wl-1"); err == nil {
		t.Fatal("Lookup accepted a corrupt record")
	}
}

func TestFileStoreLookupFailsClosedOnUnknownField(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "wl-1"), []byte(`{"phase":"Built","extra":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Lookup("wl-1"); err == nil {
		t.Fatal("Lookup accepted a record with an unrecognized field")
	}
}

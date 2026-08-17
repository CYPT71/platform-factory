package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/CYPT71/platform-factory/internal/microvm"
)

func TestBootBundleIsDeterministic(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	first, err := NewBootBundle(digest, "", digest, []string{"console=ttyS0"}, map[string]string{"b": "2", "a": "1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewBootBundle(digest, "", digest, []string{"console=ttyS0"}, map[string]string{"a": "1", "b": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest || first.Digest == "" {
		t.Fatalf("non-deterministic bundle: %s %s", first.Digest, second.Digest)
	}
	if err := ValidateBootBundle(first); err != nil {
		t.Fatalf("validate canonical bundle: %v", err)
	}
}

func TestBootBundleRejectsEveryUnpinnedInput(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for name, inputs := range map[string][3]string{
		"kernel": {"unpinned", "", digest},
		"rootfs": {digest, "", "unpinned"},
		"initrd": {digest, "unpinned", digest},
		"hex":    {"sha256:" + strings.Repeat("z", 64), "", digest},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewBootBundle(inputs[0], inputs[1], inputs[2], nil, nil); err == nil {
				t.Fatal("unpinned boot input accepted")
			}
		})
	}
	if got := cloneSorted(nil); got != nil {
		t.Fatalf("empty metadata=%v", got)
	}
}

func TestValidateBootBundleRejectsTamperingAndNonCanonicalInputs(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	bundle, err := NewBootBundle(digest, "", digest, []string{"console=ttyS0"}, map[string]string{"profile": "secure"})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]api.BootBundle{
		"api version": func() api.BootBundle {
			changed := bundle
			changed.APIVersion = "platform-factory.dev/vmm/v999"
			return changed
		}(),
		"digest": func() api.BootBundle {
			changed := bundle
			changed.Digest = "sha256:" + strings.Repeat("b", 64)
			return changed
		}(),
		"kernel": func() api.BootBundle {
			changed := bundle
			changed.Kernel = "unpinned"
			return changed
		}(),
		"metadata": func() api.BootBundle {
			changed := bundle
			changed.Metadata = map[string]string{"profile": "tampered"}
			return changed
		}(),
		"command line": func() api.BootBundle {
			changed := bundle
			changed.CommandLine = []string{"init=/tampered"}
			return changed
		}(),
	}
	for name, candidate := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateBootBundle(candidate); err == nil {
				t.Fatal("tampered boot bundle accepted")
			}
		})
	}
}

func TestFileStateStoreValidationCancellationAndCorruption(t *testing.T) {
	if _, err := NewFileStateStore(""); err == nil {
		t.Fatal("empty state directory accepted")
	}
	parentFile := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStateStore(filepath.Join(parentFile, "state")); err == nil {
		t.Fatal("state directory created below a file")
	}
	root := t.TempDir()
	store, err := NewFileStateStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Put(ctx, api.MachineStatus{ID: "machine"}); err == nil {
		t.Fatal("cancelled put succeeded")
	}
	if _, _, err := store.Get(ctx, "machine"); err == nil {
		t.Fatal("cancelled get succeeded")
	}
	if err := store.Delete(ctx, "machine"); err == nil {
		t.Fatal("cancelled delete succeeded")
	}
	for _, invalid := range []string{"", "UPPER", "../escape", strings.Repeat("a", 64)} {
		if err := store.Put(context.Background(), api.MachineStatus{ID: invalid}); err == nil {
			t.Fatalf("put accepted %q", invalid)
		}
		if _, _, err := store.Get(context.Background(), invalid); err == nil {
			t.Fatalf("get accepted %q", invalid)
		}
		if err := store.Delete(context.Background(), invalid); err == nil {
			t.Fatalf("delete accepted %q", invalid)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(context.Background(), "broken"); err == nil {
		t.Fatal("corrupt state accepted")
	}
	if _, err := store.List(context.Background()); err == nil {
		t.Fatal("list ignored corrupt state")
	}
	if err := os.Remove(filepath.Join(root, "broken.json")); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), "missing"); err != nil {
		t.Fatal(err)
	}
}

func TestFileStateStoreRejectsRegularFileAsStateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStateStore(dir); err == nil {
		t.Fatal("regular file accepted as state directory")
	}
}

func TestFileStateStoreSurfacesMkdirAllFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permission bits")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	if _, err := NewFileStateStore(filepath.Join(parent, "missing", "state")); err == nil {
		t.Fatal("unwritable parent accepted")
	}
}

func TestFileStateStorePutRejectsOversizedStatus(t *testing.T) {
	store, err := NewFileStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	huge := api.MachineStatus{ID: "machine", Error: strings.Repeat("x", 1<<20)}
	if err := store.Put(context.Background(), huge); err == nil {
		t.Fatal("oversized status accepted")
	}
}

func TestFileStateStoreGetRejectsOversizedStoredState(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStateStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// Written directly, bypassing Put's own size check, so this proves Get
	// independently refuses an oversized file already on disk (for
	// example from an older, less strict version writing state).
	huge := make([]byte, (1<<20)+1)
	if err := os.WriteFile(filepath.Join(root, "machine.json"), huge, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(context.Background(), "machine"); err == nil {
		t.Fatal("oversized stored state accepted")
	}
}

func TestFileStateStoreLifecycle(t *testing.T) {
	store, err := NewFileStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Unix(1, 0).UTC()
	status := api.MachineStatus{ID: "example", State: api.StateCreated, CreatedAt: now, UpdatedAt: now}
	if err := store.Put(context.Background(), status); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Get(context.Background(), "example")
	if err != nil || !found || got.State != api.StateCreated {
		t.Fatalf("got=%+v found=%v err=%v", got, found, err)
	}
	list, err := store.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	if err := store.Delete(context.Background(), "example"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.Get(context.Background(), "example"); found {
		t.Fatal("deleted machine remains")
	}
}

// onceLiveContext reports no error the first time Err is called (the
// check List makes before reading the directory) and context.Canceled on
// every call after, so a test can prove List's *per-entry* cancellation
// check (distinct from its upfront one) actually runs.
type onceLiveContext struct {
	context.Context
	calls int
}

func (c *onceLiveContext) Err() error {
	c.calls++
	if c.calls <= 1 {
		return nil
	}
	return context.Canceled
}

func TestFileStateStoreListHonorsCancellationDuringIteration(t *testing.T) {
	store, err := NewFileStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Put(context.Background(), api.MachineStatus{ID: "machine"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(&onceLiveContext{Context: context.Background()}); err == nil {
		t.Fatal("cancellation during iteration ignored")
	}
}

func TestFileStateStoreDeleteSurfacesNonNotExistRemoveFailure(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStateStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// "machine.json" exists as a non-empty directory, so Remove fails
	// with something other than "does not exist".
	if err := os.MkdirAll(filepath.Join(root, "machine.json", "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), "machine"); err == nil {
		t.Fatal("non-empty directory collision accepted")
	}
}

func TestFileStateStoreListSkipsUnrelatedEntriesAndHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStateStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	status := api.MachineStatus{ID: "listed", State: api.StateCreated}
	if err := store.Put(context.Background(), status); err != nil {
		t.Fatal(err)
	}
	list, err := store.List(context.Background())
	if err != nil || len(list) != 1 || list[0].ID != "listed" {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.List(ctx); err == nil {
		t.Fatal("cancelled list succeeded")
	}
}

func TestFileStateStoreListOrdersMultipleEntries(t *testing.T) {
	store, err := NewFileStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, id := range []string{"zebra", "apple", "mango"} {
		if err := store.Put(context.Background(), api.MachineStatus{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].ID != "apple" || list[1].ID != "mango" || list[2].ID != "zebra" {
		t.Fatalf("list not sorted by ID: %+v", list)
	}
}

func TestFileStateStoreGetSurfacesNonNotExistOpenFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
	root := t.TempDir()
	store, err := NewFileStateStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Put(context.Background(), api.MachineStatus{ID: "machine"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "machine.json")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, _, err := store.Get(context.Background(), "machine"); err == nil {
		t.Fatal("unreadable state file accepted")
	}
}

// syncDirectory and createTemporary are exercised directly (this file is in
// package vmm) because triggering their specific open failures through Put
// or Delete would also require the preceding write/rename to succeed, which
// the same permission change would otherwise block.
func TestFileStateStoreSyncDirectorySurfacesOpenFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
	root := t.TempDir()
	store, err := NewFileStateStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := os.Chmod(root, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	if err := store.syncDirectory(); err == nil {
		t.Fatal("syncDirectory succeeded against an inaccessible directory")
	}
}

func TestFileStateStoreCreateTemporarySurfacesNonExistOpenFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
	root := t.TempDir()
	store, err := NewFileStateStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := os.Chmod(root, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	if _, _, err := store.createTemporary(); err == nil {
		t.Fatal("createTemporary succeeded against an inaccessible directory")
	}
}

func TestFileStateStoreRejectsSymlinkRootAndMismatchedDocumentID(t *testing.T) {
	parent := t.TempDir()
	realDirectory := filepath.Join(parent, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(realDirectory, link); err == nil {
		if store, err := NewFileStateStore(link); err == nil {
			_ = store.Close()
			t.Fatal("symbolic-link state root accepted")
		}
	}

	store, err := NewFileStateStore(realDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mismatched, err := json.Marshal(api.MachineStatus{ID: "other", State: api.StateCreated})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDirectory, "expected.json"), mismatched, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(context.Background(), "expected"); err == nil ||
		!strings.Contains(err.Error(), "document id") {
		t.Fatalf("mismatched state id err=%v", err)
	}
}

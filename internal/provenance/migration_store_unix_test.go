//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package provenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/core"
)

func TestMigrationExecutionStorePinsRootAgainstSubstitution(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "records")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	pinned := filepath.Join(parent, "pinned")
	attacker := filepath.Join(parent, "attacker")
	if err := os.Mkdir(attacker, 0o700); err != nil {
		t.Fatal(err)
	}
	oldHook := afterMigrationProvenanceRootOpen
	afterMigrationProvenanceRootOpen = func() {
		if err := os.Rename(root, pinned); err != nil {
			t.Fatalf("rename pinned root: %v", err)
		}
		if err := os.Symlink(attacker, root); err != nil {
			t.Fatalf("substitute root: %v", err)
		}
	}
	t.Cleanup(func() { afterMigrationProvenanceRootOpen = oldHook })
	store, err := NewMigrationExecutionStore(root)
	afterMigrationProvenanceRootOpen = oldHook
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RecordExecution(context.Background(), validMigrationEvidence("migration-pinned")); err != nil {
		t.Fatal(err)
	}
	attackerEntries, err := os.ReadDir(attacker)
	if err != nil {
		t.Fatal(err)
	}
	if len(attackerEntries) != 0 {
		t.Fatalf("record escaped into substituted root: %v", attackerEntries)
	}
	pinnedEntries, err := os.ReadDir(pinned)
	if err != nil || len(pinnedEntries) != 1 {
		t.Fatalf("pinned entries=%v err=%v", pinnedEntries, err)
	}
}

func TestMigrationExecutionStorePinsRootAgainstParentSubstitution(t *testing.T) {
	grandparent := t.TempDir()
	parent := filepath.Join(grandparent, "parent")
	root := filepath.Join(parent, "records")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	pinnedParent := filepath.Join(grandparent, "pinned-parent")
	attackerParent := filepath.Join(grandparent, "attacker-parent")
	if err := os.MkdirAll(filepath.Join(attackerParent, "records"), 0o700); err != nil {
		t.Fatal(err)
	}
	oldHook := afterMigrationProvenanceRootOpen
	afterMigrationProvenanceRootOpen = func() {
		if err := os.Rename(parent, pinnedParent); err != nil {
			t.Fatalf("rename parent: %v", err)
		}
		if err := os.Symlink(attackerParent, parent); err != nil {
			t.Fatalf("substitute parent: %v", err)
		}
	}
	t.Cleanup(func() { afterMigrationProvenanceRootOpen = oldHook })
	store, err := NewMigrationExecutionStore(root)
	afterMigrationProvenanceRootOpen = oldHook
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RecordExecution(context.Background(), validMigrationEvidence("migration-parent-pinned")); err != nil {
		t.Fatal(err)
	}
	attackerEntries, err := os.ReadDir(filepath.Join(attackerParent, "records"))
	if err != nil || len(attackerEntries) != 0 {
		t.Fatalf("attacker entries=%v err=%v", attackerEntries, err)
	}
	pinnedEntries, err := os.ReadDir(filepath.Join(pinnedParent, "records"))
	if err != nil || len(pinnedEntries) != 1 {
		t.Fatalf("pinned entries=%v err=%v", pinnedEntries, err)
	}
}

func TestMigrationExecutionStoreLoadRejectsSymlinkPermissionsAndOversize(t *testing.T) {
	tests := []struct {
		name  string
		write func(t *testing.T, path string)
		want  string
	}{
		{"symlink", func(t *testing.T, path string) {
			t.Helper()
			target := filepath.Join(filepath.Dir(filepath.Dir(path)), "migration-provenance-symlink-target")
			if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}, "read record"},
		{"permissions", func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "regular 0600"},
		{"oversize", func(t *testing.T, path string) {
			t.Helper()
			file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(maxMigrationExecutionRecordSize + 1); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}, "size limit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, strings.Repeat("a", 64))
			tc.write(t, path)
			_, err := NewMigrationExecutionStore(root)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestMigrationExecutionStoreCollisionValidatesExistingDescriptor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, path string)
	}{
		{"symlink", func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(filepath.Dir(path)), "migration-provenance-collision-target")
			if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"permissions", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"oversize", func(t *testing.T, path string) {
			file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(maxMigrationExecutionRecordSize + 1); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewMigrationExecutionStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			evidence := validMigrationEvidence(core.OperationID("migration-collision-" + tc.name))
			key := migrationExecutionKey(evidence.TraceID, evidence.OperationID)
			tc.prepare(t, filepath.Join(root, key))
			if err := store.RecordExecution(context.Background(), evidence); err == nil || !strings.Contains(err.Error(), "conflicting concurrent evidence") {
				t.Fatalf("unsafe collision err=%v", err)
			}
		})
	}
}

func TestMigrationExecutionStoreFilesAreStrictlyPrivate(t *testing.T) {
	root := t.TempDir()
	store, err := NewMigrationExecutionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	evidence := validMigrationEvidence("migration-mode")
	if err := store.RecordExecution(context.Background(), evidence); err != nil {
		t.Fatal(err)
	}
	key := migrationExecutionKey(evidence.TraceID, evidence.OperationID)
	info, err := os.Stat(filepath.Join(root, key))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	data, err := os.ReadFile(filepath.Join(root, key))
	if err != nil {
		t.Fatal(err)
	}
	var record MigrationExecutionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
}

//go:build !windows

package marketplace

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestHashEntrypointRejectsAnIrregularFile(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "plugin.fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := hashEntrypoint(root, "plugin.fifo"); err == nil {
		t.Fatal("expected an error for an entrypoint that is neither a regular file nor a directory")
	}
}

//go:build linux

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOpenContainerIOEmptyPathIsDevNull(t *testing.T) {
	f, closeFn, err := openContainerIO("")
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if _, err := f.WriteString("discarded\n"); err != nil {
		t.Fatalf("write to /dev/null fallback: %v", err)
	}
}

// containerd creates the named pipe at this path and blocks a reader on it
// before platform-factory-shim's Create handler ever runs; openContainerIO must
// be able to open the write end without also needing to hold a read end
// itself (see task_service.go's doc comment on why that used to matter).
func TestOpenContainerIOOpensExistingPipeForWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	f, closeFn, err := openContainerIO(path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if _, err := f.WriteString("guest output\n"); err != nil {
		t.Fatalf("write to fifo: %v", err)
	}
}

//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package provenance

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// afterMigrationProvenanceRootOpen is a test seam for rename/substitution
// attacks. Production leaves it as a no-op.
var afterMigrationProvenanceRootOpen = func() {}

func openMigrationProvenanceRoot(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("migration provenance: open pinned root: %w", err)
	}
	root := os.NewFile(uintptr(fd), path)
	if root == nil {
		_ = unix.Close(fd)
		return nil, errors.New("migration provenance: wrap root descriptor")
	}
	info, err := root.Stat()
	if err != nil || !info.IsDir() {
		_ = root.Close()
		return nil, errors.New("migration provenance: root must be a real directory")
	}
	if info.Mode().Perm() != 0o700 {
		if err := root.Chmod(0o700); err != nil {
			_ = root.Close()
			return nil, fmt.Errorf("migration provenance: protect pinned root: %w", err)
		}
	}
	afterMigrationProvenanceRootOpen()
	return root, nil
}

func readMigrationProvenanceDir(root *os.File) ([]os.DirEntry, error) {
	if root == nil {
		return nil, errors.New("migration provenance: store is closed")
	}
	fd, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return nil, fmt.Errorf("migration provenance: duplicate root descriptor: %w", err)
	}
	dir := os.NewFile(uintptr(fd), root.Name())
	if dir == nil {
		_ = unix.Close(fd)
		return nil, errors.New("migration provenance: wrap duplicated root descriptor")
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return entries, nil
}

func readMigrationProvenanceRecord(root *os.File, name string, limit int64) ([]byte, error) {
	if root == nil {
		return nil, errors.New("migration provenance: store is closed")
	}
	fd, err := unix.Openat(int(root.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("migration provenance: wrap record descriptor")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, errors.New("record must be a regular 0600 file")
	}
	if info.Size() > limit {
		return nil, errors.New("record exceeds size limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("record exceeds size limit")
	}
	return data, nil
}

func appendMigrationProvenanceRecord(root *os.File, key string, data []byte) error {
	if root == nil {
		return errors.New("migration provenance: store is closed")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Errorf("migration provenance: generate temp name: %w", err)
	}
	tmpName := ".migration-" + hex.EncodeToString(random[:])
	fd, err := unix.Openat(int(root.Fd()), tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("migration provenance: create temp record: %w", err)
	}
	tmp := os.NewFile(uintptr(fd), tmpName)
	if tmp == nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(int(root.Fd()), tmpName, 0)
		return errors.New("migration provenance: wrap temp descriptor")
	}
	cleanup := func() { _ = unix.Unlinkat(int(root.Fd()), tmpName, 0) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("migration provenance: write temp record: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("migration provenance: sync temp record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("migration provenance: close temp record: %w", err)
	}
	if err := unix.Linkat(int(root.Fd()), tmpName, int(root.Fd()), key, 0); err != nil {
		cleanup()
		if errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("%w: %q", errMigrationExecutionExists, key)
		}
		return fmt.Errorf("migration provenance: publish record: %w", err)
	}
	cleanup()
	if err := unix.Fsync(int(root.Fd())); err != nil {
		return fmt.Errorf("migration provenance: sync root: %w", err)
	}
	return nil
}

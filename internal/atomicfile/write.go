// Package atomicfile publishes complete files without exposing partial writes.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write replaces name using a same-directory temporary file. When durable is
// true both file contents and the directory entry are synced before return.
func Write(dir, name string, data []byte, mode os.FileMode, durable bool) error {
	if name == "" || filepath.Base(name) != name {
		return fmt.Errorf("atomic file: invalid flat name %q", name)
	}
	temporary, err := os.CreateTemp(dir, ".atomic-*")
	if err != nil {
		return err
	}
	path := temporary.Name()
	defer os.Remove(path)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if durable {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(path, filepath.Join(dir, name)); err != nil {
		return err
	}
	if !durable {
		return nil
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}

package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// executeFreezeStep publishes redirected output only after the producer has
// succeeded. Root-relative operations prevent lockfile symlinks escaping the
// project. Failure leaves the previous lock and its permissions intact.
func executeFreezeStep(loadedRoot string, step freezeStep, diagnostics io.Writer, execute projectExecutor) error {
	if step.output == "" {
		return execute(step.args[0], step.args[1:], loadedRoot, diagnostics, diagnostics)
	}
	relative := filepath.Clean(step.output)
	if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("freeze output must stay inside the project")
	}
	root, err := os.OpenRoot(loadedRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	mode := os.FileMode(0o644)
	if info, err := root.Lstat(relative); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("freeze output must be a regular file, not a symlink or directory")
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(relative)
	if err := root.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	temporary := filepath.Join(parent, ".pf-freeze-"+rand.Text())
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	defer file.Close()
	if err := execute(step.args[0], step.args[1:], loadedRoot, file, diagnostics); err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Rename(temporary, relative); err != nil {
		return fmt.Errorf("replace freeze output: %w", err)
	}
	return nil
}

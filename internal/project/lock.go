package project

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/CYPT71/platform-factory/internal/strictjson"
)

const CurrentLockVersion = 1

// Lock pins nondeterministic inputs observed when a project is initialized.
type Lock struct {
	Version   int    `json:"version"`
	GitCommit string `json:"git_commit,omitempty"`
}

// LoadLock strictly loads a bounded v1 project lockfile.
func LoadLock(filename string) (Lock, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return Lock{}, fmt.Errorf("open project lock: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Lock{}, errors.New("project lock must be a regular file, not a symlink")
	}
	if info.Size() > 1<<20 {
		return Lock{}, errors.New("project lock exceeds 1 MiB")
	}
	file, err := os.Open(filename)
	if err != nil {
		return Lock{}, fmt.Errorf("open project lock: %w", err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return Lock{}, fmt.Errorf("read project lock: %w", err)
	}
	if len(raw) > 1<<20 {
		return Lock{}, errors.New("project lock exceeds 1 MiB")
	}
	var lock Lock
	if err := strictjson.Decode(raw, &lock); err != nil {
		return Lock{}, fmt.Errorf("decode project lock: %w", err)
	}
	if err := lock.Validate(); err != nil {
		return Lock{}, err
	}
	return lock, nil
}

// Validate rejects schemas or pins this build cannot interpret safely.
func (lock Lock) Validate() error {
	if lock.Version > CurrentLockVersion {
		return fmt.Errorf("lock version %d is newer than this platform-factory supports (max %d); upgrade platform-factory", lock.Version, CurrentLockVersion)
	}
	if lock.Version != CurrentLockVersion {
		return fmt.Errorf("unsupported project lock version %d", lock.Version)
	}
	if lock.GitCommit != "" && (len(lock.GitCommit) > 128 || strings.ContainsAny(lock.GitCommit, " \t\r\n\x00")) {
		return errors.New("project lock git_commit must be a single NUL-free token of at most 128 bytes")
	}
	return nil
}

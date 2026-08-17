package marketplace

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/CYPT71/platform-factory/internal/atomicfile"
	"github.com/CYPT71/platform-factory/internal/strictjson"
)

const (
	sourcesVersion  = 1
	sourcesFileName = "marketplace-sources.json"
	maxSourcesBytes = 1 << 20
)

// Sources is the list of Git repositories the marketplace tracks. It is
// the only thing an operator curates by hand - everything else (names,
// descriptions, versions, checksums) is derived from what those
// repositories themselves publish via SyncSource.
type Sources struct {
	Repositories []string `json:"repositories"`
}

// Add records repository if it is not already tracked. Returns true if
// it was newly added.
func (s *Sources) Add(repository string) bool {
	for _, existing := range s.Repositories {
		if existing == repository {
			return false
		}
	}
	s.Repositories = append(s.Repositories, repository)
	sort.Strings(s.Repositories)
	return true
}

// Remove untracks repository. Returns true if it was present.
func (s *Sources) Remove(repository string) bool {
	for i, existing := range s.Repositories {
		if existing == repository {
			s.Repositories = append(s.Repositories[:i:i], s.Repositories[i+1:]...)
			return true
		}
	}
	return false
}

// LoadSources reads the tracked-repository list. A missing file returns
// an empty Sources, not an error.
func LoadSources(path string) (*Sources, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Sources{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("marketplace: read sources: %w", err)
	}
	if len(data) > maxSourcesBytes {
		return nil, errors.New("marketplace: sources file exceeds 1 MiB")
	}
	var file struct {
		Version      int      `json:"version"`
		Repositories []string `json:"repositories"`
	}
	if err := strictjson.Decode(data, &file); err != nil {
		return nil, fmt.Errorf("marketplace: decode sources: %w", err)
	}
	if file.Version != sourcesVersion {
		return nil, fmt.Errorf("marketplace: unsupported sources version %d", file.Version)
	}
	return &Sources{Repositories: file.Repositories}, nil
}

// Save atomically persists the tracked-repository list.
func (s *Sources) Save(path string) error {
	sort.Strings(s.Repositories)
	data, err := json.MarshalIndent(struct {
		Version      int      `json:"version"`
		Repositories []string `json:"repositories"`
	}{Version: sourcesVersion, Repositories: s.Repositories}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("marketplace: create sources directory: %w", err)
	}
	return atomicfile.Write(dir, filepath.Base(path), data, 0o600, true)
}

// DefaultSourcesPath mirrors DefaultIndexPath, sitting next to it.
func DefaultSourcesPath() (string, error) {
	indexPath, err := DefaultIndexPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(indexPath), sourcesFileName), nil
}

// SyncAll syncs every tracked repository into index in place. A single
// unreachable or misbehaving repository is recorded in the returned error
// map, not fatal to syncing the rest.
func SyncAll(ctx context.Context, index *Index, sources *Sources) (results []SyncResult, failures map[string]error) {
	return SyncAllWithKeys(ctx, index, sources, nil)
}

// SyncAllWithKeys syncs every source and cryptographically verifies releases
// against trustedKeys.
func SyncAllWithKeys(ctx context.Context, index *Index, sources *Sources, trustedKeys []ed25519.PublicKey) (results []SyncResult, failures map[string]error) {
	return SyncAllWithOptions(ctx, index, sources, trustedKeys, 0)
}

// SyncAllWithOptions is SyncAllWithKeys with one addition: when
// perRepositoryTimeout is non-zero, each repository's own sync (its `git
// ls-remote` plus a shallow clone per tag) is bounded by its own
// context.WithTimeout instead of sharing ctx's single deadline across the
// whole batch - a repository whose Git host hangs times out and is
// recorded in failures without starving every repository after it in the
// list. Zero means "no extra bound beyond ctx", the exact behavior
// SyncAllWithKeys already had.
func SyncAllWithOptions(ctx context.Context, index *Index, sources *Sources, trustedKeys []ed25519.PublicKey, perRepositoryTimeout time.Duration) (results []SyncResult, failures map[string]error) {
	failures = map[string]error{}
	for _, repository := range sources.Repositories {
		existing, _ := findByRepository(index, repository)
		result, err := syncOneWithTimeout(ctx, repository, existing, trustedKeys, perRepositoryTimeout)
		if err != nil {
			failures[repository] = err
			continue
		}
		index.Upsert(result.Plugin)
		results = append(results, result)
	}
	return results, failures
}

// syncOneWithTimeout runs SyncSourceWithKeys under its own bounded
// context when timeout is non-zero, so one slow repository cannot
// consume the whole batch's time budget; cancel runs before returning
// either way; there is nothing left pending on repoCtx once
// SyncSourceWithKeys itself has returned.
func syncOneWithTimeout(ctx context.Context, repository string, existing PluginEntry, trustedKeys []ed25519.PublicKey, timeout time.Duration) (SyncResult, error) {
	repoCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		repoCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return SyncSourceWithKeys(repoCtx, repository, existing, trustedKeys)
}

func findByRepository(index *Index, repository string) (PluginEntry, bool) {
	for _, plugin := range index.Plugins {
		if plugin.Repository == repository {
			return plugin, true
		}
	}
	return PluginEntry{}, false
}

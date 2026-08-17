package marketplace

import (
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
	indexVersion  = 1
	maxIndexBytes = 64 << 20
	fileName      = "marketplace-index.json"
)

// ReleaseEntry is one tagged, indexed release of a plugin.
type ReleaseEntry struct {
	Version       string      `json:"version"`
	Tag           string      `json:"tag"`
	Checksum      string      `json:"checksum"` // sha256:<hex> of the entrypoint bytes at this tag
	Compatibility []string    `json:"compatibility,omitempty"`
	Permissions   Permissions `json:"permissions,omitempty"`
	Verified      bool        `json:"verified"`
	PublishedAt   time.Time   `json:"published_at"`
}

// PluginEntry indexes metadata and releases without embedding plugin content.
type PluginEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Author      string   `json:"author,omitempty"`
	Repository  string   `json:"repository"`
	Tags        []string `json:"tags,omitempty"`

	Releases      []ReleaseEntry `json:"releases"`
	LatestVersion string         `json:"latest_version"`

	Downloads int       `json:"downloads"`
	SyncedAt  time.Time `json:"synced_at"`
}

// Release returns the entry for version, if indexed.
func (p PluginEntry) Release(version string) (ReleaseEntry, bool) {
	for _, release := range p.Releases {
		if release.Version == version {
			return release, true
		}
	}
	return ReleaseEntry{}, false
}

// Latest returns the entry for LatestVersion.
func (p PluginEntry) Latest() (ReleaseEntry, bool) {
	return p.Release(p.LatestVersion)
}

// Index is a local snapshot of tracked repositories.
type Index struct {
	Plugins []PluginEntry `json:"plugins"`
}

type indexFile struct {
	Version int           `json:"version"`
	Plugins []PluginEntry `json:"plugins"`
}

// Plugin looks up one plugin by name.
func (idx *Index) Plugin(name string) (PluginEntry, bool) {
	for _, plugin := range idx.Plugins {
		if plugin.Name == name {
			return plugin, true
		}
	}
	return PluginEntry{}, false
}

// Upsert inserts or replaces an entry and preserves stable order.
func (idx *Index) Upsert(entry PluginEntry) {
	for i, existing := range idx.Plugins {
		if existing.Name == entry.Name {
			idx.Plugins[i] = entry
			return
		}
	}
	idx.Plugins = append(idx.Plugins, entry)
	sort.Slice(idx.Plugins, func(i, j int) bool { return idx.Plugins[i].Name < idx.Plugins[j].Name })
}

// LoadIndex strictly decodes an index; a missing file yields an empty index.
func LoadIndex(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Index{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("marketplace: read index: %w", err)
	}
	if len(data) > maxIndexBytes {
		return nil, errors.New("marketplace: index exceeds 64 MiB")
	}
	var file indexFile
	if err := strictjson.Decode(data, &file); err != nil {
		return nil, fmt.Errorf("marketplace: decode index: %w", err)
	}
	if file.Version != indexVersion {
		return nil, fmt.Errorf("marketplace: unsupported index version %d", file.Version)
	}
	for _, plugin := range file.Plugins {
		if plugin.Name == "" || plugin.Repository == "" {
			return nil, errors.New("marketplace: index entry missing name or repository")
		}
	}
	return &Index{Plugins: file.Plugins}, nil
}

// Save atomically persists the index in stable order.
func (idx *Index) Save(path string) error {
	sort.Slice(idx.Plugins, func(i, j int) bool { return idx.Plugins[i].Name < idx.Plugins[j].Name })
	data, err := json.MarshalIndent(indexFile{Version: indexVersion, Plugins: idx.Plugins}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("marketplace: create index directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("marketplace: index path must not be a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicfile.Write(dir, filepath.Base(path), data, 0o600, true)
}

// DefaultIndexPath returns the configurable per-user index path.
func DefaultIndexPath() (string, error) {
	if dir := os.Getenv("PLATFORM_FACTORY_MARKETPLACE_DIR"); dir != "" {
		return filepath.Join(dir, fileName), nil
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(config, "platform-factory", "marketplace", fileName), nil
}

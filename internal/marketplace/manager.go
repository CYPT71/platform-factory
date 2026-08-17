package marketplace

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/CYPT71/platform-factory/internal/atomicfile"
	"github.com/CYPT71/platform-factory/internal/strictjson"
	"golang.org/x/mod/semver"
)

const (
	installedStateVersion = 1
	installedStateFile    = "installed.json"
)

// InstalledPlugin records verified local plugin content.
type InstalledPlugin struct {
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Repository  string    `json:"repository"`
	Tag         string    `json:"tag"`
	Checksum    string    `json:"checksum"`
	Entrypoint  string    `json:"entrypoint"`
	InstalledAt time.Time `json:"installed_at"`
}

type installedStateFileShape struct {
	Version int               `json:"version"`
	Plugins []InstalledPlugin `json:"plugins"`
}

// Manager verifies and installs releases from an Index.
type Manager struct {
	// Dir holds installed plugins and installed.json.
	Dir string
	// HostVersion gates compatibility when it is valid SemVer.
	HostVersion string
	// TrustedKeys verifies signed manifests.
	TrustedKeys []ed25519.PublicKey
	// AllowUnsigned relaxes signatures, never checksums.
	AllowUnsigned bool
}

// Installed lists every currently installed plugin.
func (m *Manager) Installed() ([]InstalledPlugin, error) {
	state, err := m.loadState()
	if err != nil {
		return nil, err
	}
	return state.Plugins, nil
}

// InstalledPlugin looks up one installed plugin by name.
func (m *Manager) InstalledPlugin(name string) (InstalledPlugin, bool, error) {
	state, err := m.loadState()
	if err != nil {
		return InstalledPlugin{}, false, err
	}
	for _, plugin := range state.Plugins {
		if plugin.Name == name {
			return plugin, true, nil
		}
	}
	return InstalledPlugin{}, false, nil
}

// Install places version, or the latest version when empty.
func (m *Manager) Install(ctx context.Context, index *Index, name, version string) (InstalledPlugin, error) {
	if _, already, err := m.InstalledPlugin(name); err != nil {
		return InstalledPlugin{}, err
	} else if already {
		return InstalledPlugin{}, fmt.Errorf("marketplace: %q is already installed; use Update", name)
	}
	return m.installOrUpdate(ctx, index, name, version)
}

// Update replaces an installed plugin with version, or the latest when empty.
func (m *Manager) Update(ctx context.Context, index *Index, name, version string) (InstalledPlugin, error) {
	if _, already, err := m.InstalledPlugin(name); err != nil {
		return InstalledPlugin{}, err
	} else if !already {
		return InstalledPlugin{}, fmt.Errorf("marketplace: %q is not installed; use Install", name)
	}
	return m.installOrUpdate(ctx, index, name, version)
}

func (m *Manager) installOrUpdate(ctx context.Context, index *Index, name, version string) (InstalledPlugin, error) {
	if m.Dir == "" {
		return InstalledPlugin{}, errors.New("marketplace: manager directory is required")
	}
	plugin, ok := index.Plugin(name)
	if !ok {
		return InstalledPlugin{}, fmt.Errorf("marketplace: %q is not in the index; run sync first", name)
	}
	if version == "" {
		version = plugin.LatestVersion
	}
	release, ok := plugin.Release(version)
	if !ok {
		return InstalledPlugin{}, fmt.Errorf("marketplace: %q has no indexed release %q", name, version)
	}

	workdir, err := os.MkdirTemp("", "platform-factory-marketplace-install-*")
	if err != nil {
		return InstalledPlugin{}, err
	}
	defer os.RemoveAll(workdir)
	if _, err := runGit(ctx, "", "clone", "--depth", "1", "--branch", release.Tag, "--single-branch", "--", plugin.Repository, workdir); err != nil {
		return InstalledPlugin{}, fmt.Errorf("marketplace: fetch %s@%s: %w", name, release.Tag, err)
	}
	file, err := os.Open(filepath.Join(workdir, ManifestFileName))
	if err != nil {
		return InstalledPlugin{}, fmt.Errorf("marketplace: open %s: %w", ManifestFileName, err)
	}
	manifest, err := DecodeManifest(file)
	closeErr := file.Close()
	if err != nil {
		return InstalledPlugin{}, err
	}
	if closeErr != nil {
		return InstalledPlugin{}, closeErr
	}
	if manifest.Name != name || normalizeVersion(manifest.Version) != normalizeVersion(version) {
		return InstalledPlugin{}, fmt.Errorf("marketplace: fetched manifest %s@%s does not match requested %s@%s",
			manifest.Name, manifest.Version, name, version)
	}
	if m.HostVersion != "" && semver.IsValid(normalizeVersion(m.HostVersion)) {
		compatible, err := manifest.CompatibleWith(m.HostVersion)
		if err != nil {
			return InstalledPlugin{}, err
		}
		if !compatible {
			return InstalledPlugin{}, fmt.Errorf("marketplace: %s@%s is not compatible with host version %s (constraints: %v)",
				name, version, m.HostVersion, manifest.Compatibility)
		}
	}
	checksum, err := hashEntrypoint(workdir, manifest.Entrypoint)
	if err != nil {
		return InstalledPlugin{}, err
	}
	if checksum != release.Checksum {
		return InstalledPlugin{}, fmt.Errorf("marketplace: %s@%s checksum %s does not match the indexed checksum %s "+
			"(the tag may have moved since it was indexed; re-sync and review before trusting it)",
			name, version, checksum, release.Checksum)
	}
	if manifest.Signature == nil {
		if !m.AllowUnsigned {
			return InstalledPlugin{}, fmt.Errorf("marketplace: %s@%s is unsigned; set AllowUnsigned to accept it", name, version)
		}
	} else if err := manifest.VerifySignature(m.TrustedKeys); err != nil {
		return InstalledPlugin{}, fmt.Errorf("marketplace: %s@%s: %w", name, version, err)
	}

	installed := InstalledPlugin{
		Name: name, Version: version, Repository: plugin.Repository, Tag: release.Tag,
		Checksum: checksum, Entrypoint: manifest.Entrypoint, InstalledAt: time.Now().UTC(),
	}
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return InstalledPlugin{}, err
	}
	if err := atomicReplaceDir(m.Dir, name, workdir); err != nil {
		return InstalledPlugin{}, err
	}
	if err := m.updateState(func(state *installedStateFileShape) {
		for i, existing := range state.Plugins {
			if existing.Name == name {
				state.Plugins[i] = installed
				return
			}
		}
		state.Plugins = append(state.Plugins, installed)
	}); err != nil {
		return InstalledPlugin{}, err
	}
	return installed, nil
}

// Remove deletes an installed plugin's files and forgets it. It is not an
// error to remove a plugin that isn't installed - Remove is idempotent.
func (m *Manager) Remove(name string) error {
	if m.Dir == "" {
		return errors.New("marketplace: manager directory is required")
	}
	if err := os.RemoveAll(filepath.Join(m.Dir, name)); err != nil {
		return err
	}
	return m.updateState(func(state *installedStateFileShape) {
		kept := state.Plugins[:0]
		for _, existing := range state.Plugins {
			if existing.Name != name {
				kept = append(kept, existing)
			}
		}
		state.Plugins = kept
	})
}

func (m *Manager) statePath() string {
	return filepath.Join(m.Dir, installedStateFile)
}

func (m *Manager) loadState() (installedStateFileShape, error) {
	data, err := os.ReadFile(m.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return installedStateFileShape{Version: installedStateVersion}, nil
	}
	if err != nil {
		return installedStateFileShape{}, err
	}
	var state installedStateFileShape
	if err := strictjson.Decode(data, &state); err != nil {
		return installedStateFileShape{}, fmt.Errorf("marketplace: decode installed state: %w", err)
	}
	if state.Version != installedStateVersion {
		return installedStateFileShape{}, fmt.Errorf("marketplace: unsupported installed-state version %d", state.Version)
	}
	sort.Slice(state.Plugins, func(i, j int) bool { return state.Plugins[i].Name < state.Plugins[j].Name })
	return state, nil
}

func (m *Manager) updateState(mutate func(*installedStateFileShape)) error {
	state, err := m.loadState()
	if err != nil {
		return err
	}
	mutate(&state)
	sort.Slice(state.Plugins, func(i, j int) bool { return state.Plugins[i].Name < state.Plugins[j].Name })
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return err
	}
	return atomicfile.Write(m.Dir, installedStateFile, data, 0o600, true)
}

// atomicReplaceDir moves source's plugin.yaml and manifest-declared
// entrypoint into root/name, atomically from the point of view of any
// reader: root/name either has the complete previous install or the
// complete new one, never a partial mix. A failure after the backup is
// taken restores it rather than leaving root/name missing.
func atomicReplaceDir(root, name, source string) error {
	final := filepath.Join(root, name)
	staged, err := os.MkdirTemp(root, ".install-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staged)
	if err := copyTree(source, staged); err != nil {
		return err
	}

	backup := final + ".previous"
	_ = os.RemoveAll(backup)
	hadPrevious := false
	if _, err := os.Lstat(final); err == nil {
		if err := os.Rename(final, backup); err != nil {
			return err
		}
		hadPrevious = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(staged, final); err != nil {
		if hadPrevious {
			_ = os.Rename(backup, final)
		}
		return err
	}
	if hadPrevious {
		_ = os.RemoveAll(backup)
	}
	return nil
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if d.IsDir() {
			if d.Name() == ".git" && rel != "." {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("marketplace: refusing to install non-regular file %s", rel)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

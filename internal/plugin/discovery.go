package plugin

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Discovered pairs a plugin directory with its validated manifest.
type Discovered struct {
	Dir      string
	Manifest Manifest
}

// Discover scans the immediate subdirectories of root for plugin
// manifests, in deterministic name order. A subdirectory without a
// manifest is skipped silently; an invalid manifest or a duplicate
// plugin name fails discovery, because silently dropping a plugin the
// operator installed would hide a policy-relevant error.
func Discover(root string) ([]Discovered, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("plugin discovery: %w", err)
	}
	byName := map[string]string{}
	var result []Discovered
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if _, statErr := os.Stat(filepath.Join(dir, ManifestFileName)); errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		manifest, err := LoadManifest(dir)
		if err != nil {
			return nil, fmt.Errorf("plugin discovery: %s: %w", entry.Name(), err)
		}
		if previous, duplicate := byName[manifest.Name]; duplicate {
			return nil, fmt.Errorf("plugin discovery: plugin name %q appears in both %s and %s", manifest.Name, previous, dir)
		}
		byName[manifest.Name] = dir
		result = append(result, Discovered{Dir: dir, Manifest: manifest})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Manifest.Name < result[j].Manifest.Name })
	return result, nil
}

// LoadPublicKey reads a PEM PKIX Ed25519 public key from filename, the
// format produced by openssl pkey -pubout for Ed25519 keys.
func LoadPublicKey(filename string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("plugin key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("plugin key: %s does not contain a PEM PUBLIC KEY block", filename)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("plugin key: %w", err)
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("plugin key: %s is not an Ed25519 public key", filename)
	}
	return key, nil
}

// DiscoverAndRegister scans for plugins and registers them in a registry.
// This enables capability-based dispatch: instead of asking "Are you KubeVirt?",
// the core can ask "Who can provide deployment.apply?".
// Sanetizer-todo item 12: Capability negotiation.
func DiscoverAndRegister(root string) (*Registry, error) {
	discovered, err := Discover(root)
	if err != nil {
		return nil, err
	}

	registry := NewRegistry()
	for _, d := range discovered {
		registry.registerDiscovered(d.Manifest)
	}
	return registry, nil
}

// DiscoverAndRegisterGlobal is a convenience function that discovers plugins
// from the given root directory and registers them in the global registry.
// Sanetizer-todo item 12: Capability negotiation.
func DiscoverAndRegisterGlobal(root string) error {
	registry, err := DiscoverAndRegister(root)
	if err != nil {
		return err
	}
	for _, d := range registry.GetAllPlugins() {
		if manifest, ok := registry.GetManifest(d); ok {
			RegisterGlobal(manifest)
		}
	}
	return nil
}

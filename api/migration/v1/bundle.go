package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const BundleFormatVersion = "platform-factory.dev/migration-bundle/v1"

type BundleDocument struct {
	Path   string `json:"path" yaml:"path"`
	Digest string `json:"digest" yaml:"digest"`
}

type BundleManifest struct {
	FormatVersion string           `json:"format_version" yaml:"format_version"`
	Documents     []BundleDocument `json:"documents" yaml:"documents"`
}

func WriteBundle(root string, documents map[string][]byte) (BundleManifest, error) {
	if root == "" || len(documents) == 0 {
		return BundleManifest{}, errors.New("migration bundle: root and documents are required")
	}
	manifest := BundleManifest{FormatVersion: BundleFormatVersion}
	paths := make([]string, 0, len(documents))
	for name := range documents {
		if err := validateBundlePath(name); err != nil {
			return BundleManifest{}, err
		}
		if name == "environment.yaml" {
			return BundleManifest{}, errors.New("migration bundle: environment.yaml is reserved for the manifest")
		}
		paths = append(paths, name)
	}
	sort.Strings(paths)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return BundleManifest{}, fmt.Errorf("migration bundle: create root: %w", err)
	}
	for _, name := range paths {
		data := documents[name]
		if secretValue(string(data)) {
			return BundleManifest{}, fmt.Errorf("migration bundle: document %s contains secret-like content", name)
		}
		sum := sha256.Sum256(data)
		manifest.Documents = append(manifest.Documents, BundleDocument{Path: name, Digest: "sha256:" + hex.EncodeToString(sum[:])})
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := rejectBundlePathSymlinks(root, name, true); err != nil {
			return BundleManifest{}, err
		}
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			return BundleManifest{}, fmt.Errorf("migration bundle: create document directory: %w", err)
		}
		if err := rejectBundlePathSymlinks(root, name, true); err != nil {
			return BundleManifest{}, err
		}
		if err := os.WriteFile(filename, data, 0o600); err != nil {
			return BundleManifest{}, fmt.Errorf("migration bundle: write %s: %w", name, err)
		}
	}
	encoded, err := MarshalYAML(&manifest)
	if err != nil {
		return BundleManifest{}, err
	}
	if err := rejectBundlePathSymlinks(root, "environment.yaml", true); err != nil {
		return BundleManifest{}, err
	}
	if err := os.WriteFile(filepath.Join(root, "environment.yaml"), encoded, 0o600); err != nil {
		return BundleManifest{}, fmt.Errorf("migration bundle: write manifest: %w", err)
	}
	return manifest, nil
}

func ReadBundle(root string) (BundleManifest, map[string][]byte, error) {
	manifestPath := filepath.Join(root, "environment.yaml")
	if err := rejectBundleSymlink(manifestPath); err != nil {
		return BundleManifest{}, nil, err
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return BundleManifest{}, nil, fmt.Errorf("migration bundle: read manifest: %w", err)
	}
	var manifest BundleManifest
	if err := UnmarshalYAML(raw, &manifest); err != nil {
		return BundleManifest{}, nil, err
	}
	if manifest.FormatVersion != BundleFormatVersion || len(manifest.Documents) == 0 {
		return BundleManifest{}, nil, errors.New("migration bundle: invalid manifest")
	}
	documents := make(map[string][]byte, len(manifest.Documents))
	previous := ""
	for _, document := range manifest.Documents {
		if err := validateBundlePath(document.Path); err != nil {
			return BundleManifest{}, nil, err
		}
		if previous != "" && document.Path <= previous {
			return BundleManifest{}, nil, errors.New("migration bundle: documents are not uniquely sorted")
		}
		previous = document.Path
		filename := filepath.Join(root, filepath.FromSlash(document.Path))
		if err := rejectBundlePathSymlinks(root, document.Path, false); err != nil {
			return BundleManifest{}, nil, err
		}
		if err := rejectBundleSymlink(filename); err != nil {
			return BundleManifest{}, nil, err
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return BundleManifest{}, nil, fmt.Errorf("migration bundle: read %s: %w", document.Path, err)
		}
		sum := sha256.Sum256(data)
		want := "sha256:" + hex.EncodeToString(sum[:])
		if document.Digest != want {
			return BundleManifest{}, nil, fmt.Errorf("migration bundle: digest mismatch for %s", document.Path)
		}
		documents[document.Path] = data
	}
	return manifest, documents, nil
}

func validateBundlePath(name string) error {
	if name == "" || strings.Contains(name, "\\") || filepath.IsAbs(name) || filepath.Clean(name) != filepath.FromSlash(name) || name == "." || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return fmt.Errorf("migration bundle: unsafe relative path %q", name)
	}
	return nil
}

func rejectBundlePathSymlinks(root, relative string, allowMissing bool) error {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("migration bundle: inspect root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return errors.New("migration bundle: root is not a real directory")
	}
	current := root
	for _, component := range strings.Split(filepath.FromSlash(relative), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && allowMissing {
			return nil
		}
		if err != nil {
			return fmt.Errorf("migration bundle: inspect path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("migration bundle: path %s contains a symlink", relative)
		}
	}
	return nil
}

func rejectBundleSymlink(filename string) error {
	info, err := os.Lstat(filename)
	if err != nil {
		return fmt.Errorf("migration bundle: inspect %s: %w", filename, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("migration bundle: %s is not a regular file", filename)
	}
	return nil
}

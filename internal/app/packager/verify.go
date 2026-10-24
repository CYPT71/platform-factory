package packager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Verify checks an extracted distribution without running its executables.
// The manifest establishes integrity, not publisher identity: obtain the archive
// and its trusted checksum/signature through an authenticated release channel.
func Verify(directory string) (Manifest, error) {
	var manifest Manifest
	info, err := os.Lstat(directory)
	if err != nil {
		return manifest, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return manifest, errors.New("distribution must be a real directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return manifest, err
	}
	defer root.Close()
	file, err := root.Open("MANIFEST.json")
	if err != nil {
		return manifest, fmt.Errorf("read distribution manifest: %w", err)
	}
	defer file.Close()
	manifestInfo, err := file.Stat()
	if err != nil {
		return manifest, err
	}
	if !manifestInfo.Mode().IsRegular() || manifestInfo.Size() > 8<<20 {
		return manifest, errors.New("invalid distribution manifest file or size")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 8<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, fmt.Errorf("decode distribution manifest: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return manifest, errors.New("distribution manifest contains trailing data")
	}
	if manifest.APIVersion != "platform-factory.dev/distribution/v1" || manifest.Version == "" {
		return manifest, errors.New("invalid distribution identity")
	}
	switch manifest.Target {
	case "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64":
	default:
		return manifest, fmt.Errorf("unsupported distribution target %q", manifest.Target)
	}
	expected := make(map[string]ManifestFile, len(manifest.Files))
	for _, entry := range manifest.Files {
		name := entry.Path
		if name == "" || name == "." || name == "MANIFEST.json" || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\:\x00") {
			return manifest, fmt.Errorf("unsafe distribution path %q", name)
		}
		if _, exists := expected[name]; exists {
			return manifest, fmt.Errorf("duplicate distribution path %q", name)
		}
		digest, err := hex.DecodeString(strings.TrimPrefix(entry.SHA256, "sha256:"))
		if err != nil || len(digest) != sha256.Size || !strings.HasPrefix(entry.SHA256, "sha256:") || entry.Size < 0 {
			return manifest, fmt.Errorf("invalid digest or size for %s", name)
		}
		expected[name] = entry
	}
	suffix := ""
	if strings.HasPrefix(manifest.Target, "windows/") {
		suffix = ".exe"
	}
	for _, name := range []string{"bin/platform-factory" + suffix, "bin/pf" + suffix, "environment.json", "INSTALL.txt"} {
		if _, ok := expected[name]; !ok {
			return manifest, fmt.Errorf("distribution missing required entry %s", name)
		}
	}
	err = filepath.WalkDir(directory, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("distribution contains symlink %s", filename)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported distribution file %s", filename)
		}
		relative, err := filepath.Rel(directory, filename)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if name == "MANIFEST.json" {
			return nil
		}
		expectedFile, ok := expected[name]
		if !ok {
			return fmt.Errorf("unlisted distribution file %s", name)
		}
		f, err := root.Open(name)
		if err != nil {
			return err
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if size != expectedFile.Size || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != expectedFile.SHA256 {
			return fmt.Errorf("distribution integrity mismatch: %s", name)
		}
		delete(expected, name)
		return nil
	})
	if err != nil {
		return manifest, err
	}
	if len(expected) != 0 {
		return manifest, errors.New("distribution contains missing files")
	}
	return manifest, nil
}

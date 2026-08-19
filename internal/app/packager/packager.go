// Package packager is the application-layer service behind
// cmd/platform-factory-packager: assembling a deterministic, relocatable
// release archive (tar.gz on Unix, zip on Windows) from an environment
// produced by scripts/local/bootstrap.sh, with a SHA-256 manifest and an
// atomic, no-overwrite install. cmd/platform-factory-packager/main.go
// only parses flags; every actual collect/pack/install step lives here,
// testable without going through the CLI at all.
package packager

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Manifest is the deterministic file listing written into every
// package as MANIFEST.json.
type Manifest struct {
	APIVersion string         `json:"api_version"`
	Target     string         `json:"target"`
	Version    string         `json:"version"`
	Files      []ManifestFile `json:"files"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Package assembles a deterministic release archive from env (an
// environment produced by scripts/local/bootstrap.sh) and installs it
// atomically to out. out must not already exist - packaging never
// silently replaces a prior release archive.
func Package(env, out string) error {
	raw, err := os.ReadFile(filepath.Join(env, "environment.json"))
	if err != nil {
		return fmt.Errorf("read environment manifest: %w", err)
	}
	var metadata struct {
		TargetOS   string `json:"target_os"`
		TargetArch string `json:"target_arch"`
		Version    string `json:"version"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return err
	}
	target := metadata.TargetOS + "/" + metadata.TargetArch
	entries, err := collect(env)
	if err != nil {
		return err
	}
	platformName, aliasName := "bin/platform-factory", "bin/pf"
	if metadata.TargetOS == "windows" {
		platformName, aliasName = "bin/platform-factory.exe", "bin/pf.exe"
	}
	for _, item := range entries {
		if item.name == platformName {
			entries = append(entries, entry{name: aliasName, data: append([]byte(nil), item.data...), mode: 0o755})
			break
		}
	}
	installText := "Platform Factory " + metadata.Version + " (" + target + ")\n\n"
	if metadata.TargetOS == "windows" {
		installText += "Extract this ZIP, then run `.\\Activate.ps1` in PowerShell or `activate.bat` in cmd.exe.\nRun `pf version` to verify the installation.\n"
	} else {
		installText += "Extract this archive, then run `source ./activate`.\nRun `pf version` to verify the installation.\n"
	}
	entries = append(entries, entry{name: "INSTALL.txt", data: []byte(installText), mode: 0o644})
	m := Manifest{APIVersion: "platform-factory.dev/distribution/v1", Target: target, Version: metadata.Version}
	for _, e := range entries {
		sum := sha256.Sum256(e.data)
		m.Files = append(m.Files, ManifestFile{Path: e.name, SHA256: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(e.data))})
	}
	manifestBytes, _ := json.MarshalIndent(m, "", "  ")
	entries = append(entries, entry{name: "MANIFEST.json", data: append(manifestBytes, '\n'), mode: 0o644})
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(out), ".package-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if metadata.TargetOS == "windows" {
		err = writeZip(temporary, entries)
	} else {
		err = writeTarGz(temporary, entries)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if _, err := os.Lstat(out); err == nil {
		return fmt.Errorf("refusing to overwrite %s", out)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(name, out); err != nil {
		return err
	}
	return nil
}

type entry struct {
	name string
	data []byte
	mode fs.FileMode
}

func collect(root string) ([]entry, error) {
	var result []entry
	for _, base := range []string{"bin", "activate", "Activate.ps1", "activate.bat", "deactivate.bat", "environment.json"} {
		path := filepath.Join(root, base)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing symlink %s", path)
		}
		if info.IsDir() {
			children, err := os.ReadDir(path)
			if err != nil {
				return nil, err
			}
			for _, child := range children {
				if child.Type()&os.ModeSymlink != 0 || !child.Type().IsRegular() {
					return nil, fmt.Errorf("unsupported package entry %s", child.Name())
				}
				data, err := os.ReadFile(filepath.Join(path, child.Name()))
				if err != nil {
					return nil, err
				}
				result = append(result, entry{name: "bin/" + child.Name(), data: data, mode: 0o755})
			}
		} else if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			result = append(result, entry{name: base, data: data, mode: 0o644})
		} else {
			return nil, fmt.Errorf("unsupported package entry %s", path)
		}
	}
	return result, nil
}

var epoch = time.Unix(0, 0).UTC()

func writeTarGz(w io.Writer, entries []entry) error {
	gz, _ := gzip.NewWriterLevel(w, gzip.BestCompression)
	gz.Header.ModTime = epoch
	gz.Header.OS = 255
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: int64(e.mode.Perm()), Size: int64(len(e.data)), ModTime: epoch, AccessTime: epoch, ChangeTime: epoch, Uid: 0, Gid: 0, Format: tar.FormatPAX}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := tw.Write(e.data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func writeZip(w io.Writer, entries []entry) error {
	zw := zip.NewWriter(w)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		h.SetMode(e.mode)
		h.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
		f, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		if _, err := f.Write(e.data); err != nil {
			return err
		}
	}
	return zw.Close()
}

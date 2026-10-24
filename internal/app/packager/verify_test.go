package packager

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func extractedDistribution(t *testing.T) string {
	t.Helper()
	env := t.TempDir()
	if err := os.Mkdir(filepath.Join(env, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"bin/platform-factory": "core", "bin/platform-factory-kubernetes": "integration", "environment.json": `{"target_os":"linux","target_arch":"amd64","version":"v1.0.0"}`} {
		if err := os.WriteFile(filepath.Join(env, name), []byte(data), 0755); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(t.TempDir(), "release.tar.gz")
	if err := Package(env, archive); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	dir := t.TempDir()
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(dir, filepath.FromSlash(header.Name))
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, data, 0755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestVerifyExtractedReleaseAndRejectChanges(t *testing.T) {
	for _, kind := range []string{"valid", "tampered", "missing", "extra", "symlink", "manifest-symlink", "duplicate", "traversal", "trailing", "target"} {
		t.Run(kind, func(t *testing.T) {
			dir := extractedDistribution(t)
			file := filepath.Join(dir, "bin/platform-factory-kubernetes")
			manifestPath := filepath.Join(dir, "MANIFEST.json")
			raw, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			var manifest Manifest
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "tampered":
				err = os.WriteFile(file, []byte("modified"), 0755)
			case "missing":
				err = os.Remove(file)
			case "extra":
				err = os.WriteFile(filepath.Join(dir, "bin/injected"), []byte("extra"), 0755)
			case "symlink", "manifest-symlink":
				if kind == "manifest-symlink" {
					file = manifestPath
				}
				if err = os.Remove(file); err == nil {
					err = os.Symlink(filepath.Join(t.TempDir(), "outside"), file)
				}
				if err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			case "duplicate":
				manifest.Files = append(manifest.Files, manifest.Files[0])
			case "traversal":
				manifest.Files[0].Path = "../outside"
			case "target":
				manifest.Target = "alien/unknown"
			case "trailing":
				err = os.WriteFile(manifestPath, append(raw, []byte("{}")...), 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "duplicate" || kind == "traversal" || kind == "target" {
				raw, err = json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(manifestPath, raw, 0644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Verify(dir)
			if kind == "valid" {
				if err != nil || got.Target != "linux/amd64" {
					t.Fatalf("manifest=%+v err=%v", got, err)
				}
			} else if err == nil {
				t.Fatalf("accepted %s", kind)
			}
		})
	}
}

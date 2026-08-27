package packager

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPackageIsDeterministicAndRefusesOverwrite(t *testing.T) {
	env := t.TempDir()
	if err := os.Mkdir(filepath.Join(env, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env, "bin", "platform-factory"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env, "environment.json"), []byte(`{"target_os":"linux","target_arch":"amd64","version":"v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(t.TempDir(), "first.tar.gz")
	second := filepath.Join(t.TempDir(), "second.tar.gz")
	if err := Package(env, first); err != nil {
		t.Fatal(err)
	}
	if err := Package(env, second); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(first)
	b, _ := os.ReadFile(second)
	if sha256.Sum256(a) != sha256.Sum256(b) {
		t.Fatal("packages differ")
	}
	if err := Package(env, first); err == nil {
		t.Fatal("overwrite accepted")
	}
	gz, err := gzip.NewReader(bytesReader(a))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names[h.Name] = true
	}
	for _, name := range []string{"bin/platform-factory", "bin/pf", "environment.json", "INSTALL.txt", "MANIFEST.json"} {
		if !names[name] {
			t.Fatalf("missing %s", name)
		}
	}
}

func TestPackageWritesAZipArchiveForWindowsTargets(t *testing.T) {
	env := t.TempDir()
	if err := os.Mkdir(filepath.Join(env, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env, "bin", "platform-factory.exe"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env, "environment.json"), []byte(`{"target_os":"windows","target_arch":"amd64","version":"v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "package.zip")
	if err := Package(env, out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, name := range []string{"bin/platform-factory.exe", "bin/pf.exe", "environment.json", "INSTALL.txt", "MANIFEST.json"} {
		if !names[name] {
			t.Fatalf("missing %s in %v", name, names)
		}
	}
}

func TestPackageRejectsSymlink(t *testing.T) {
	env := t.TempDir()
	if err := os.Mkdir(filepath.Join(env, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("outside", filepath.Join(env, "bin", "platform-factory")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env, "environment.json"), []byte(`{"target_os":"linux","target_arch":"amd64","version":"v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Package(env, filepath.Join(t.TempDir(), "x.tar.gz")); err == nil {
		t.Fatal("symlink accepted")
	}
}

type byteReader struct {
	data   []byte
	offset int
}

func bytesReader(data []byte) *byteReader { return &byteReader{data: data} }
func (r *byteReader) Read(p []byte) (int, error) {
	if r.offset == len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

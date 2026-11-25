package main

import (
	"archive/tar"
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
	if err := run([]string{"--env", env, "--out", first}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--env", env, "--out", second}); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(first)
	b, _ := os.ReadFile(second)
	if sha256.Sum256(a) != sha256.Sum256(b) {
		t.Fatal("packages differ")
	}
	if err := run([]string{"--env", env, "--out", first}); err == nil {
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
	if err := run([]string{"--env", env, "--out", filepath.Join(t.TempDir(), "x.tar.gz")}); err == nil {
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

func TestRunRejectsBadFlagsAndMissingArguments(t *testing.T) {
	if err := run([]string{"--unknown-flag"}); err == nil {
		t.Fatal("expected an error for an unrecognized flag")
	}
	if err := run([]string{}); err == nil {
		t.Fatal("expected an error when --env/--out are both missing")
	}
	if err := run([]string{"--env", t.TempDir()}); err == nil {
		t.Fatal("expected an error when --out is missing")
	}
	if err := run([]string{"--verify", t.TempDir(), "--out", "x.tar.gz"}); err == nil {
		t.Fatal("expected --verify combined with --out to be rejected")
	}
	if err := run([]string{"--verify", t.TempDir(), "extra-positional-arg"}); err == nil {
		t.Fatal("expected a positional argument alongside --verify to be rejected")
	}
}

func TestRunVerifyDelegatesToPackagerVerify(t *testing.T) {
	if err := run([]string{"--verify", filepath.Join(t.TempDir(), "does-not-exist")}); err == nil {
		t.Fatal("expected verifying a nonexistent distribution to fail")
	}
}

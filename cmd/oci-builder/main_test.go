package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/oci"
)

func TestRunWithoutArgumentsShowsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "-binary") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunArgumentErrors(t *testing.T) {
	for _, args := range [][]string{{"-created", "bad"}, {"-label", "bad"}, {"-unknown"}} {
		var out, err bytes.Buffer
		if code := run(args, &out, &err); code != 2 {
			t.Fatalf("run(%v) = %d, stderr=%s", args, code, err.String())
		}
	}
}

func TestRunBuildsLayout(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := run([]string{"-binary", binary, "-output", filepath.Join(dir, "layout"), "-arch", "amd64", "-label", "a=b"}, &out, &stderr)
	if code != 0 || !strings.Contains(out.String(), "created OCI layout") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
	}
	var labels labelFlags
	if err := labels.Set("a=b"); err != nil || labels.String() != "" {
		t.Fatal("label flag failed")
	}
}

func TestRunBuildsLayoutWithExtraFiles(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "legacy")
	if err := os.WriteFile(binary, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(dir, "libfoo.so")
	if err := os.WriteFile(lib, []byte("lib"), 0755); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := run([]string{
		"-binary", binary, "-output", filepath.Join(dir, "layout"), "-arch", "amd64",
		"-extra-file", "/lib/libfoo.so=" + lib,
	}, &out, &stderr)
	if code != 0 || !strings.Contains(out.String(), "created OCI layout") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
	}
}

func TestRunRejectsMalformedExtraFile(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := run([]string{"-binary", binary, "-output", filepath.Join(dir, "layout"), "-extra-file", "not-a-pair"}, &out, &stderr)
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunCompose(t *testing.T) {
	dir := t.TempDir()
	build := func(name, architecture string) string {
		binary := filepath.Join(dir, name)
		if err := os.WriteFile(binary, []byte(name), 0o755); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(dir, name+"-layout")
		if _, err := oci.Build(oci.Options{
			Binary: binary, Output: output, Architecture: architecture,
			ImageName: "example/service", Tag: "v1",
		}); err != nil {
			t.Fatal(err)
		}
		return output
	}
	amd64 := build("amd64", "amd64")
	arm64 := build("arm64", "arm64")
	var stdout, stderr bytes.Buffer
	output := filepath.Join(dir, "multi")
	if code := run([]string{"compose", "-output", output, amd64, arm64}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "2 manifests") {
		t.Fatalf("stdout=%s", stdout.String())
	}
	if code := run([]string{"compose", "-output", filepath.Join(dir, "bad"), amd64}, &stdout, &stderr); code != 2 {
		t.Fatalf("single input code=%d", code)
	}
}

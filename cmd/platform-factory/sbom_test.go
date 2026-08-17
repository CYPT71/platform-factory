package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/sbom"
)

func TestRunSBOMGeneratesInventoryJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "app.sh"), "#!/bin/sh\necho hi\n")
	writeFile(t, filepath.Join(dir, "data", "notes.txt"), "hello world\n")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"sbom", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var document sbom.Document
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("decode sbom: %v (out=%s)", err, stdout.String())
	}
	if len(document.Components) != 2 {
		t.Fatalf("want 2 components, got %d: %+v", len(document.Components), document.Components)
	}
	// Components are sorted by name; every one carries a real digest.
	previous := ""
	for _, component := range document.Components {
		if !strings.HasPrefix(component.Digest, "sha256:") || component.Size == 0 {
			t.Fatalf("component %q missing digest/size: %+v", component.Name, component)
		}
		if component.Name <= previous {
			t.Fatalf("components not sorted: %q after %q", component.Name, previous)
		}
		previous = component.Name
	}
}

func TestRunSBOMDeterministicAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "b.txt"), "second\n")
	writeFile(t, filepath.Join(dir, "a.txt"), "first\n")

	first := sbomOutput(t, dir)
	second := sbomOutput(t, dir)
	if first != second {
		t.Fatalf("sbom not deterministic:\n%s\n---\n%s", first, second)
	}
}

func TestRunSBOMText(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "one.txt"), "x\n")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"sbom", "--format", "text", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1 components") ||
		!strings.Contains(stdout.String(), "sha256:") {
		t.Fatalf("unexpected text output: %s", stdout.String())
	}
}

func TestRunSBOMSingleFileArgument(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "solo.txt")
	writeFile(t, file, "solo\n")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"sbom", file}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var document sbom.Document
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(document.Components) != 1 || document.Components[0].Name != filepath.Clean(file) {
		t.Fatalf("want single component named %q: %+v", filepath.Clean(file), document.Components)
	}
}

func TestRunSBOMErrors(t *testing.T) {
	empty := t.TempDir()
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no arguments", []string{"sbom"}, 2},
		{"bad format", []string{"sbom", "--format", "yaml", empty}, 2},
		{"missing path", []string{"sbom", filepath.Join(empty, "absent")}, 1},
		{"empty directory", []string{"sbom", empty}, 1},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(testCase.args, &stdout, &stderr); code != testCase.want {
				t.Fatalf("args=%v code=%d want=%d stderr=%s", testCase.args, code, testCase.want, stderr.String())
			}
		})
	}
}

func sbomOutput(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run(append([]string{"sbom"}, args...), &stdout, &stderr); code != 0 {
		t.Fatalf("sbom failed: code=%d stderr=%s", code, stderr.String())
	}
	return stdout.String()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/layout"
	"github.com/CYPT71/secure-oci-base/internal/project"
)

// TestLaunchBuildsAndRunsSupportedProjectEndToEnd is the first v1 exit
// criterion: from a supported project directory, launch freezes, builds
// and runs without any manual edit, and hands the container runtime a
// hardened invocation.
func TestLaunchBuildsAndRunsSupportedProjectEndToEnd(t *testing.T) {
	root := t.TempDir()
	writeProjectTestFile(t, filepath.Join(root, ".config_image.yaml"),
		"language: compiled\nartifact: app\nimage: example/e2e\ntag: v1\n", 0o644)
	writeProjectTestFile(t, filepath.Join(root, "app"), "static executable payload", 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var runtimeArgs []string
	containerExecute := func(name string, args []string, _ io.Reader, _, _ io.Writer) error {
		runtimeArgs = append([]string{name}, args...)
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := runProject([]string{"launch", root}, &stdout, &stderr, execute, containerExecute, nil); code != 0 {
		t.Fatalf("launch code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".platform-factory", "freeze.lock.json")); err != nil {
		t.Fatalf("freeze inventory missing: %v", err)
	}
	report, err := layout.Verify(filepath.Join(root, ".platform-factory", "image"))
	if err != nil {
		t.Fatal(err)
	}
	if report.Platforms[0].Reference != "example/e2e:v1" {
		t.Fatalf("report=%+v", report)
	}
	argv := strings.Join(runtimeArgs, " ")
	for _, want := range []string{
		"docker", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--network=none",
	} {
		if !strings.Contains(argv, want) {
			t.Fatalf("runtime argv missing %s: %s", want, argv)
		}
	}
}

// TestProjectDoubleBuildProducesIdenticalDigest is the second v1 exit
// criterion: two clean builds of the same project produce the same
// digest, and platform-factory diff confirms byte-level equality.
func TestProjectDoubleBuildProducesIdenticalDigest(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\nimage: example/repro\ntag: v1\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "reproducible payload", 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	build := func() string {
		var stdout, stderr bytes.Buffer
		digest, code := buildProject(loaded, &stdout, &stderr, execute)
		if code != 0 {
			t.Fatalf("build code=%d stderr=%s", code, stderr.String())
		}
		return digest
	}
	first := build()
	firstCopy := filepath.Join(t.TempDir(), "first")
	if err := os.Rename(loaded.Output(), firstCopy); err != nil {
		t.Fatal(err)
	}
	second := build()
	if first == "" || first != second {
		t.Fatalf("digests differ: %q vs %q", first, second)
	}
	diffReport, err := layout.Diff(firstCopy, loaded.Output())
	if err != nil {
		t.Fatal(err)
	}
	if !diffReport.Equal {
		var text bytes.Buffer
		diffReport.WriteText(&text)
		t.Fatalf("clean rebuilds diverge:\n%s", text.String())
	}
	if _, err := project.Load(loaded.File); err != nil {
		t.Fatal(err)
	}
}

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/sdk/langplugin"
)

// withTestPluginDir points sdk/langplugin's registry at a fresh temp
// directory for the duration of the test, so these tests never touch
// the real user's home directory.
func withTestPluginDir(t *testing.T) {
	t.Helper()
	t.Setenv("PLATFORM_FACTORY_LANG_PLUGIN_DIR", t.TempDir())
}

func writeFakePluginBinary(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho fake\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// writeMinimalPluginModule writes the smallest possible standalone Go
// module (no dependencies, so `go build` works fully offline) into dir,
// for tests that exercise prepareSource's directory-source build path.
func writeMinimalPluginModule(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/testplugin\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunPluginLoadWithFromInstallsAnyName(t *testing.T) {
	withTestPluginDir(t)
	source := filepath.Join(t.TempDir(), "my-plugin-binary")
	writeFakePluginBinary(t, source)

	var stdout, stderr bytes.Buffer
	code := runPlugin([]string{"load", "--from", source, "acme-lang"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "loaded acme-lang") {
		t.Fatalf("stdout=%s", stdout.String())
	}

	var listOut, listErr bytes.Buffer
	if code := runPlugin([]string{"list"}, &listOut, &listErr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, listErr.String())
	}
	if want := fmt.Sprintf("%-10s %s", "acme-lang", "loaded (custom)"); !strings.Contains(listOut.String(), want) {
		t.Fatalf("list output=%s", listOut.String())
	}
}

func TestRunPluginLoadWithFromDirectoryBuildsAndInstalls(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}
	withTestPluginDir(t)
	dir := t.TempDir()
	writeMinimalPluginModule(t, dir)

	var stdout, stderr bytes.Buffer
	code := runPlugin([]string{"load", "--from", dir, "acme-src"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "loaded acme-src") {
		t.Fatalf("stdout=%s", stdout.String())
	}
	path, err := langplugin.Resolve("acme-src")
	if err != nil {
		t.Fatal(err)
	}
	if info, statErr := os.Stat(path); statErr != nil || info.Size() == 0 {
		t.Fatalf("info=%v err=%v", info, statErr)
	}
}

func TestRunPluginLoadWithFromDirectoryWithoutGoModErrors(t *testing.T) {
	withTestPluginDir(t)
	dir := t.TempDir() // empty, no go.mod
	var stdout, stderr bytes.Buffer
	code := runPlugin([]string{"load", "--from", dir, "acme"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected failure, stdout=%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "go.mod") {
		t.Fatalf("stderr=%s, want a hint about the missing go.mod", stderr.String())
	}
}

func TestPrepareSourceRejectsMissingPath(t *testing.T) {
	_, cleanup, err := prepareSource(filepath.Join(t.TempDir(), "does-not-exist"))
	cleanup()
	if err == nil {
		t.Fatal("expected an error for a nonexistent --from path")
	}
}

func TestRunPluginLoadWithoutFromRejectsUnknownName(t *testing.T) {
	withTestPluginDir(t)
	var stdout, stderr bytes.Buffer
	code := runPlugin([]string{"load", "not-a-real-language"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected a non-zero exit code, stdout=%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "--from") {
		t.Fatalf("stderr=%s, want a hint to use --from", stderr.String())
	}
}

func TestRunPluginLoadWithoutFromFindsBinaryNextToExecutable(t *testing.T) {
	withTestPluginDir(t)
	self, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	candidate := filepath.Join(filepath.Dir(self), "platform-factory-lang-python")
	if runtime.GOOS == "windows" {
		candidate += ".exe"
	}
	writeFakePluginBinary(t, candidate)
	defer os.Remove(candidate)

	var stdout, stderr bytes.Buffer
	code := runPlugin([]string{"load", "python"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "loaded python") {
		t.Fatalf("stdout=%s", stdout.String())
	}
}

func TestRunPluginUnloadThenListShowsNotLoaded(t *testing.T) {
	withTestPluginDir(t)
	source := filepath.Join(t.TempDir(), "plugin")
	writeFakePluginBinary(t, source)
	if code := runPlugin([]string{"load", "--from", source, "python"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("setup: load failed")
	}

	var stdout, stderr bytes.Buffer
	code := runPlugin([]string{"unload", "python"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "unloaded python") {
		t.Fatalf("stdout=%s", stdout.String())
	}

	var listOut bytes.Buffer
	if code := runPlugin([]string{"list"}, &listOut, &bytes.Buffer{}); code != 0 {
		t.Fatal("list failed")
	}
	if want := fmt.Sprintf("%-10s %s", "python", "not loaded"); !strings.Contains(listOut.String(), want) {
		t.Fatalf("list output=%s", listOut.String())
	}
}

func TestRunPluginListShowsAllBuiltinsByDefault(t *testing.T) {
	withTestPluginDir(t)
	var stdout, stderr bytes.Buffer
	if code := runPlugin([]string{"list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, lang := range builtinLanguages {
		if !strings.Contains(stdout.String(), lang) {
			t.Fatalf("stdout=%s, missing built-in language %q", stdout.String(), lang)
		}
	}
}

func TestRunPluginUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runPlugin([]string{"frobnicate"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d", code)
	}
}

func TestRunPluginNoArgsPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runPlugin(nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stderr.String(), "platform-factory plugin") {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func TestRunPluginHelpPrintsUsageToStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runPlugin([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "platform-factory plugin") {
		t.Fatalf("stdout=%s", stdout.String())
	}
}

func TestRunDispatchesToPlugin(t *testing.T) {
	withTestPluginDir(t)
	source := filepath.Join(t.TempDir(), "plugin")
	writeFakePluginBinary(t, source)

	var stdout, stderr bytes.Buffer
	code := run([]string{"plugin", "load", "--from", source, "python"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestPluginManagementFailsClosedOnInvalidRegistryState(t *testing.T) {
	for _, test := range []struct {
		name     string
		args     []string
		setup    func(*testing.T)
		wantCode int
	}{
		{"load-help", []string{"load", "--help"}, withTestPluginDir, 0},
		{"load-args", []string{"load"}, withTestPluginDir, 2},
		{"unload-help", []string{"unload", "--help"}, withTestPluginDir, 0},
		{"unload-args", []string{"unload"}, withTestPluginDir, 2},
		{"list-help", []string{"list", "--help"}, withTestPluginDir, 0},
		{"list-file-as-registry", []string{"list"}, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "registry-file")
			if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PLATFORM_FACTORY_LANG_PLUGIN_DIR", name)
		}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.setup(t)
			var stdout, stderr bytes.Buffer
			if code := runPlugin(test.args, &stdout, &stderr); code != test.wantCode {
				t.Fatalf("code=%d want=%d stdout=%s stderr=%s", code, test.wantCode, stdout.String(), stderr.String())
			}
		})
	}
}

func TestResolveLoadedPluginUsesManagedRegistryOnly(t *testing.T) {
	withTestPluginDir(t)
	if _, err := resolveLoadedPlugin("missing"); err == nil {
		t.Fatal("unloaded plugin resolved")
	}
	source := filepath.Join(t.TempDir(), "plugin")
	writeFakePluginBinary(t, source)
	if _, err := langplugin.Load("custom", source); err != nil {
		t.Fatal(err)
	}
	path, err := resolveLoadedPlugin("custom")
	if err != nil || !filepath.IsAbs(path) {
		t.Fatalf("path=%q err=%v", path, err)
	}
}

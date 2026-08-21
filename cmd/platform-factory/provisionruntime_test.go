package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/project"
	"github.com/CYPT71/platform-factory/sdk/langplugin"
)

// loadRuntimeTestProject writes a minimal, valid pf.yaml with no runtime
// field set (provisionRuntimeFromRoot's callers all require this - see
// runPluginProvisionRuntime's own already-has-a-runtime-field check) and
// loads it the same way the real command does.
func loadRuntimeTestProject(t *testing.T) project.Loaded {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "pf.yaml")
	writeProjectTestFile(t, configPath, "version: 1\nlanguage: python\nprofile: \"python\"\nartifact: \"main.py\"\nisolation: container\nruntime_engine: docker\ndependency_management:\n  mode: none\n", 0o644)
	writeProjectTestFile(t, filepath.Join(dir, "main.py"), "print('hi')\n", 0o644)
	loaded, err := project.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

// loadFakeLanguagePlugin points PLATFORM_FACTORY_LANG_PLUGIN_DIR at a
// fresh temp directory and registers name as loaded, so
// langplugin.Resolve(name) succeeds - the file's own content is
// irrelevant here since tests supply their own projectExecutor stub
// instead of ever actually running it.
func loadFakeLanguagePlugin(t *testing.T, name string) {
	t.Helper()
	t.Setenv("PLATFORM_FACTORY_LANG_PLUGIN_DIR", t.TempDir())
	source := filepath.Join(t.TempDir(), "plugin-binary")
	writeProjectTestFile(t, source, "#!/bin/sh\n", 0o755)
	if _, err := langplugin.Load(name, source); err != nil {
		t.Fatal(err)
	}
}

func TestRunPluginProvisionRuntimeRequiresLanguageAndImage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	for _, args := range [][]string{
		nil,
		{"--language", "python"},
		{"--image", "python@sha256:abc"},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := runPluginProvisionRuntime(context.Background(), args, &stdout, &stderr); code != 2 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
}

func TestRefreshProvisionedProjectLockPinsChangedPlanAndBase(t *testing.T) {
	loaded := loadRuntimeTestProject(t)
	raw, err := os.ReadFile(loaded.File)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := project.CanonicalManifestDigest(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(project.Lock{Version: 2, PlanDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	writeProjectTestFile(t, loaded.AdjacentLockPath(), string(encoded), 0o600)
	updated := append(raw, []byte("runtime: runtime/python\n")...)
	if err := os.WriteFile(loaded.File, updated, 0o644); err != nil {
		t.Fatal(err)
	}
	base := project.LockedInput{Name: "python@sha256:source", Digest: "sha256:" + strings.Repeat("a", 64)}
	if err := refreshProvisionedProjectLock(loaded, &base); err != nil {
		t.Fatal(err)
	}
	lock, err := project.LoadLock(loaded.AdjacentLockPath())
	if err != nil || len(lock.Bases) != 1 || lock.Bases[0] != base {
		t.Fatalf("lock=%+v err=%v", lock, err)
	}
	if err := loaded.VerifyAdjacentLock(); err != nil {
		t.Fatalf("updated plan did not verify: %v", err)
	}
}

func TestRunPluginProvisionRuntimeRejectsProjectWithExistingRuntime(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "pf.yaml"),
		"version: 1\nlanguage: python\nprofile: \"python\"\nartifact: \"main.py\"\nruntime: \"/already/set\"\n", 0o644)
	writeProjectTestFile(t, filepath.Join(dir, "main.py"), "print('hi')\n", 0o644)
	var stdout, stderr bytes.Buffer
	code := runPluginProvisionRuntime(context.Background(), []string{"--language", "python", "--image", "python@sha256:abc", "--dir", dir}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "already has a runtime field") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestRunPluginProvisionRuntimeRejectsUndiscoverableProject(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runPluginProvisionRuntime(context.Background(), []string{"--language", "python", "--image", "python@sha256:abc", "--dir", t.TempDir()}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestAutoProvisionRuntimeIsANoOpWithoutARealTerminal(t *testing.T) {
	// go test's own stdin/stdout are never a real terminal, so this
	// must return immediately without blocking on a TUI, touching the
	// network, or writing anything - the same safety net that keeps
	// every other init/build TUI prompt out of CI. profile is set
	// explicitly (real pf init output always records it) so this
	// actually reaches the isatty gate instead of returning early via
	// validateBuildCapability's own "no profile recorded" compatibility
	// path - see TestAutoProvisionRuntimeIsANoOpForCompiledLanguages for
	// that other early-return path, tested on its own.
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "pf.yaml"), "version: 1\nlanguage: python\nprofile: \"python\"\nartifact: main.py\n", 0o644)
	writeProjectTestFile(t, filepath.Join(dir, "main.py"), "print('hi')\n", 0o644)
	var stdout, stderr bytes.Buffer
	autoProvisionRuntime(context.Background(), dir, "python", &stdout, &stderr)
	loaded, err := project.Discover(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.Runtime != "" {
		t.Fatalf("expected no runtime field to be written, got %q", loaded.Config.Runtime)
	}
}

func TestAutoProvisionRuntimeIsANoOpForCompiledLanguages(t *testing.T) {
	// A compiled-language project (or any profile validateBuildCapability
	// doesn't recognize) never needs a runtime field at all - this must
	// return silently with no warning, even without a real terminal and
	// even if no plugin for "compiled" could ever be resolved.
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "pf.yaml"), "version: 1\nlanguage: compiled\nprofile: \"static\"\nartifact: app\n", 0o644)
	writeProjectTestFile(t, filepath.Join(dir, "app"), "binary", 0o755)
	var stdout, stderr bytes.Buffer
	autoProvisionRuntime(context.Background(), dir, "compiled", &stdout, &stderr)
	if stdout.String() != "" || stderr.String() != "" {
		t.Fatalf("expected no output for a compiled-language project, stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestAutoProvisionRuntimeWarnsWhenTheLanguagePluginIsNotInstalled(t *testing.T) {
	// Overriding the plugin directory to an empty one guarantees
	// langplugin.Resolve fails deterministically here, regardless of
	// what plugins happen to be installed on the machine actually
	// running this test.
	t.Setenv("PLATFORM_FACTORY_LANG_PLUGIN_DIR", t.TempDir())
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "pf.yaml"), "version: 1\nlanguage: node\nprofile: \"node\"\nartifact: app.js\n", 0o644)
	writeProjectTestFile(t, filepath.Join(dir, "app.js"), "console.log('hi')\n", 0o644)
	var stdout, stderr bytes.Buffer
	autoProvisionRuntime(context.Background(), dir, "node", &stdout, &stderr)
	if !strings.Contains(stderr.String(), "isn't installed") {
		t.Fatalf("expected a warning about the missing plugin, stderr=%q", stderr.String())
	}
	loaded, err := project.Discover(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.Runtime != "" {
		t.Fatalf("expected no runtime field to be written, got %q", loaded.Config.Runtime)
	}
}

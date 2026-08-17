package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hostplugin "github.com/CYPT71/platform-factory/internal/plugin"
)

// fixtureRepo builds a minimal repo with a real go.mod naming the main
// module and a real copy of sdk/plugin (the RPC server SDK
// pf_plugin_create's generated main.go depends on), so a plugin Create
// scaffolds can actually `go build` inside this fixture exactly the way
// a real plugins/<name> module does against the real repository -
// giving genuine end-to-end coverage of the generated source, not just
// a syntax check.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite := func(rel, content string) {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("go.mod", "module github.com/CYPT71/platform-factory\n\ngo 1.25.12\n")
	mustWrite("plugins/.keep", "")

	realSDKPluginDir := findRealSDKPluginDir(t)
	entries, err := os.ReadDir(realSDKPluginDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(realSDKPluginDir, name))
		if err != nil {
			t.Fatal(err)
		}
		mustWrite(filepath.Join("sdk/plugin", name), string(data))
	}
	return dir
}

// findRealSDKPluginDir locates this checkout's own sdk/plugin directory
// by walking up from the current working directory (internal/mcp/plugins)
// to the repo root.
func findRealSDKPluginDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Join(dir, "sdk", "plugin")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate sdk/plugin by walking up from the test's working directory")
		}
		dir = parent
	}
}

func TestValidPluginNameRejectsPathTraversalAndBadShapes(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"bun-builder", false},
		{"", true},
		{"Bun", true},
		{"-bun", true},
		{"bun/../../etc", true},
		{"bun_builder", true},
		{strings.Repeat("a", 64), true},
	}
	for _, c := range cases {
		err := validPluginName(c.name)
		if (err != nil) != c.wantErr {
			t.Errorf("validPluginName(%q) error=%v, wantErr=%v", c.name, err, c.wantErr)
		}
	}
}

func TestListPluginsClassifiesRPCAndLanguageCommandKinds(t *testing.T) {
	dir := fixtureRepo(t)
	writeManifest(t, dir, "widget-runtime", hostplugin.Manifest{
		APIVersion: hostplugin.ManifestAPIVersion, Name: "widget-runtime", Version: "1.0.0",
		Capabilities: []string{"runtime.start"}, Family: hostplugin.PluginFamilyRuntime,
		Executable: "widget-runtime-bin", Digest: zeroDigest,
	})
	mustMkdir(t, filepath.Join(dir, "plugins", "lang-widget"))
	mustWriteFile(t, filepath.Join(dir, "plugins", "lang-widget", "main.go"), "package main\n\nfunc main() {}\n")

	summaries, err := ListPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Summary{}
	for _, s := range summaries {
		byName[s.Name] = s
	}
	if byName["widget-runtime"].Kind != "rpc" {
		t.Fatalf("widget-runtime kind=%q", byName["widget-runtime"].Kind)
	}
	if byName["widget-runtime"].Family != "runtime" {
		t.Fatalf("widget-runtime family=%q", byName["widget-runtime"].Family)
	}
	if byName["lang-widget"].Kind != "language-command" {
		t.Fatalf("lang-widget kind=%q", byName["lang-widget"].Kind)
	}
	// ".keep" is a file, not a directory, and must not be reported as a
	// plugin.
	if _, found := byName[".keep"]; found {
		t.Fatal("a bare file under plugins/ must not be listed as a plugin")
	}
}

func TestInspectPluginRejectsPathTraversalInName(t *testing.T) {
	dir := fixtureRepo(t)
	if _, err := InspectPlugin(dir, "../../etc"); err == nil {
		t.Fatal("expected an error for a path-traversal plugin name")
	}
}

func TestInspectPluginReturnsManifestAndTestFiles(t *testing.T) {
	dir := fixtureRepo(t)
	writeManifest(t, dir, "widget-runtime", hostplugin.Manifest{
		APIVersion: hostplugin.ManifestAPIVersion, Name: "widget-runtime", Version: "1.0.0",
		Capabilities: []string{"runtime.start"}, Family: hostplugin.PluginFamilyRuntime,
		Executable: "widget-runtime-bin", Digest: zeroDigest,
	})
	mustWriteFile(t, filepath.Join(dir, "plugins", "widget-runtime", "main_test.go"), "package main\n")

	detail, err := InspectPlugin(dir, "widget-runtime")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Manifest == nil || detail.Manifest.Name != "widget-runtime" {
		t.Fatalf("manifest=%+v", detail.Manifest)
	}
	if len(detail.TestFiles) != 1 || detail.TestFiles[0] != "main_test.go" {
		t.Fatalf("testFiles=%v", detail.TestFiles)
	}
}

func TestCreateScaffoldsARealBuildablePlugin(t *testing.T) {
	dir := fixtureRepo(t)
	result, err := Create(context.Background(), dir, CreateRequest{
		Name:         "bun-builder",
		Description:  "Support Bun applications.",
		Capabilities: []string{"detect", "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Plugin != "bun-builder" || result.Path != "plugins/bun-builder" {
		t.Fatalf("result=%+v", result)
	}
	if len(result.Files) != 4 {
		t.Fatalf("expected 4 files written, got %v", result.Files)
	}

	manifestPath := filepath.Join(dir, "plugins", "bun-builder", "plugin.json")
	manifest, err := hostplugin.LoadManifest(filepath.Join(dir, "plugins", "bun-builder"))
	if err != nil {
		t.Fatalf("generated plugin.json does not pass Manifest.Validate(): %v (path %s)", err, manifestPath)
	}
	if manifest.Family != hostplugin.PluginFamilyCapability {
		t.Fatalf("expected the default family to be %q, got %q", hostplugin.PluginFamilyCapability, manifest.Family)
	}
	if !manifest.HasCapability("detect") || !manifest.HasCapability("build") {
		t.Fatalf("capabilities=%v", manifest.Capabilities)
	}

	report, err := Validate(context.Background(), dir, "bun-builder")
	if err != nil {
		t.Fatal(err)
	}
	if report.Build != "ok" {
		t.Fatalf("generated plugin does not build: %s", report.Build)
	}
}

func TestCreateRefusesADuplicateName(t *testing.T) {
	dir := fixtureRepo(t)
	req := CreateRequest{Name: "dup", Description: "x", Capabilities: []string{"detect"}}
	if _, err := Create(context.Background(), dir, req); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), dir, req); err == nil {
		t.Fatal("expected an error creating a plugin that already exists")
	}
}

func TestCreateRejectsAnEmptyDescription(t *testing.T) {
	dir := fixtureRepo(t)
	_, err := Create(context.Background(), dir, CreateRequest{Name: "x", Capabilities: []string{"detect"}})
	if err == nil {
		t.Fatal("expected an error for an empty description")
	}
}

func TestCreateRejectsNoCapabilities(t *testing.T) {
	dir := fixtureRepo(t)
	_, err := Create(context.Background(), dir, CreateRequest{Name: "x", Description: "d"})
	if err == nil {
		t.Fatal("expected an error for zero capabilities")
	}
}

func TestCreateToolHandlerRejectsUnknownFields(t *testing.T) {
	dir := fixtureRepo(t)
	handler := CreateToolHandler(dir)
	_, err := handler(context.Background(), json.RawMessage(`{"name":"x","description":"d","capabilities":["detect"],"bogus":true}`))
	if err == nil {
		t.Fatal("expected an error for an unknown field")
	}
}

func TestValidateReportsALanguageCommandPluginAsNotApplicable(t *testing.T) {
	dir := fixtureRepo(t)
	mustMkdir(t, filepath.Join(dir, "plugins", "lang-widget"))
	mustWriteFile(t, filepath.Join(dir, "plugins", "lang-widget", "main.go"), "package main\n\nfunc main() {}\n")

	report, err := Validate(context.Background(), dir, "lang-widget")
	if err != nil {
		t.Fatal(err)
	}
	if report.Manifest != "n/a (language-command plugin, no plugin.json)" {
		t.Fatalf("manifest=%q", report.Manifest)
	}
}

func TestValidateFlagsAnInvalidManifest(t *testing.T) {
	dir := fixtureRepo(t)
	mustMkdir(t, filepath.Join(dir, "plugins", "broken"))
	mustWriteFile(t, filepath.Join(dir, "plugins", "broken", "plugin.json"), `{"api_version":"wrong","name":"broken"}`)

	report, err := Validate(context.Background(), dir, "broken")
	if err != nil {
		t.Fatal(err)
	}
	if report.Valid {
		t.Fatal("expected Valid=false for a manifest with the wrong api_version")
	}
	if len(report.Issues) == 0 {
		t.Fatal("expected at least one issue reported")
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeManifest(t *testing.T, repoRoot, name string, manifest hostplugin.Manifest) {
	t.Helper()
	dir := filepath.Join(repoRoot, "plugins", name)
	mustMkdir(t, dir)
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(dir, hostplugin.ManifestFileName), string(encoded))
}

package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/marketplace"
	"github.com/CYPT71/platform-factory/internal/mcp/toolerror"
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
	// addPluginToGoWork appends every newly scaffolded plugin's module
	// path into this file's "use (...)" block, so a minimal one - just
	// the open/close markers, no entries yet - must exist for Create to
	// succeed, matching the real repository's own go.work shape.
	mustWrite("go.work", "go 1.25.12\n\nuse (\n\t.\n)\n")
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

func TestModuleKind(t *testing.T) {
	t.Run("no go.mod at all", func(t *testing.T) {
		if got := moduleKind(t.TempDir()); got != "none" {
			t.Fatalf("got=%q", got)
		}
	})
	t.Run("standalone go.mod with no replace directive", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteFile(t, filepath.Join(dir, "go.mod"), "module example.com/standalone\n\ngo 1.25.12\n")
		if got := moduleKind(dir); got != "standalone" {
			t.Fatalf("got=%q", got)
		}
	})
	t.Run("go.mod that replaces the main module locally", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteFile(t, filepath.Join(dir, "go.mod"),
			"module github.com/CYPT71/platform-factory/plugins/widget\n\ngo 1.25.12\n\nrequire github.com/CYPT71/platform-factory v0.0.2\n\nreplace github.com/CYPT71/platform-factory => ../..\n")
		if got := moduleKind(dir); got != "depends-on-main" {
			t.Fatalf("got=%q", got)
		}
	})
}

func TestInspectPluginRejectsPathTraversalInName(t *testing.T) {
	dir := fixtureRepo(t)
	if _, err := InspectPlugin(dir, "../../etc"); err == nil {
		t.Fatal("expected an error for a path-traversal plugin name")
	}
}

func TestInspectPluginReturnsNotFoundForAMissingPlugin(t *testing.T) {
	dir := fixtureRepo(t)
	_, err := InspectPlugin(dir, "does-not-exist")
	if err == nil {
		t.Fatal("expected an error for a plugin that does not exist")
	}
	var toolErr *toolerror.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != toolerror.ErrNotFound {
		t.Fatalf("err=%v, want a toolerror.ErrNotFound", err)
	}
}

func TestInspectPluginSurfacesAnInvalidManifest(t *testing.T) {
	dir := fixtureRepo(t)
	mustMkdir(t, filepath.Join(dir, "plugins", "broken"))
	mustWriteFile(t, filepath.Join(dir, "plugins", "broken", "plugin.json"), `{"api_version":"wrong","name":"broken"}`)

	_, err := InspectPlugin(dir, "broken")
	if err == nil {
		t.Fatal("expected an error for an invalid manifest")
	}
	var toolErr *toolerror.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != toolerror.ErrValidationFailed {
		t.Fatalf("err=%v, want a toolerror.ErrValidationFailed", err)
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
	if len(result.Files) != 6 {
		t.Fatalf("expected 6 files written, got %v", result.Files)
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
	marketplaceFile, err := os.Open(filepath.Join(dir, "plugins", "bun-builder", marketplace.ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	marketplaceManifest, err := marketplace.DecodeManifest(marketplaceFile)
	_ = marketplaceFile.Close()
	if err != nil || marketplaceManifest.Name != "bun-builder" {
		t.Fatalf("generated plugin.yaml is invalid: manifest=%+v err=%v", marketplaceManifest, err)
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

func TestCreateRejectsAnInvalidPluginName(t *testing.T) {
	dir := fixtureRepo(t)
	if _, err := Create(context.Background(), dir, CreateRequest{
		Name: "Not_Valid", Description: "d", Capabilities: []string{"detect"},
	}); err == nil {
		t.Fatal("expected an error for an invalid plugin name")
	}
}

// TestCreateSurfacesAPluginsDirectoryStatFailure covers Create's own
// os.Stat(dir) non-IsNotExist error branch (distinct from
// TestCreateRefusesADuplicateName's err==nil "already exists" case): an
// unreadable plugins/ directory makes the stat itself fail with a
// permission error rather than "not exist". Skipped when running as
// root, where permission bits are not enforced.
func TestCreateSurfacesAPluginsDirectoryStatFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	dir := fixtureRepo(t)
	pluginsRoot := filepath.Join(dir, "plugins")
	if err := os.Chmod(pluginsRoot, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(pluginsRoot, 0o755) })
	_, err := Create(context.Background(), dir, CreateRequest{
		Name: "widget", Description: "d", Capabilities: []string{"detect"},
	})
	if err == nil {
		t.Fatal("expected an error for an unreadable plugins/ directory")
	}
}

func TestCreateRejectsAFamilyThatFailsManifestValidation(t *testing.T) {
	dir := fixtureRepo(t)
	_, err := Create(context.Background(), dir, CreateRequest{
		Name: "x", Description: "d", Capabilities: []string{"detect"}, Family: "not-a-real-family",
	})
	if err == nil {
		t.Fatal("expected an error for a family that fails manifest validation")
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

func TestCreateToolHandlerRoundTripsValidArguments(t *testing.T) {
	dir := fixtureRepo(t)
	handler := CreateToolHandler(dir)
	out, err := handler(context.Background(), json.RawMessage(`{"name":"bun-builder","description":"Support Bun applications.","capabilities":["detect","build"]}`))
	if err != nil {
		t.Fatalf("handler returned an error: %v", err)
	}
	var result CreateResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("handler output is not valid JSON: %v (%s)", err, out)
	}
	if result.Plugin != "bun-builder" {
		t.Fatalf("result=%+v", result)
	}
}

func TestCreateToolHandlerRejectsInvalidJSON(t *testing.T) {
	handler := CreateToolHandler(fixtureRepo(t))
	if _, err := handler(context.Background(), json.RawMessage(`{not json`)); err == nil {
		t.Fatal("expected an error for malformed JSON arguments")
	}
}

func TestListToolHandlerReturnsEveryPlugin(t *testing.T) {
	dir := fixtureRepo(t)
	writeManifest(t, dir, "widget-runtime", hostplugin.Manifest{
		APIVersion: hostplugin.ManifestAPIVersion, Name: "widget-runtime", Version: "1.0.0",
		Capabilities: []string{"runtime.start"}, Family: hostplugin.PluginFamilyRuntime,
		Executable: "widget-runtime-bin", Digest: zeroDigest,
	})
	handler := ListToolHandler(dir)
	out, err := handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler returned an error: %v", err)
	}
	var summaries []Summary
	if err := json.Unmarshal([]byte(out), &summaries); err != nil {
		t.Fatalf("handler output is not valid JSON: %v (%s)", err, out)
	}
	if len(summaries) != 1 || summaries[0].Name != "widget-runtime" {
		t.Fatalf("summaries=%+v", summaries)
	}
}

func TestListToolHandlerSurfacesAListPluginsFailure(t *testing.T) {
	handler := ListToolHandler(filepath.Join(t.TempDir(), "does-not-exist"))
	if _, err := handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected an error for a repo with no plugins/ directory")
	}
}

func TestInspectToolHandlerSurfacesAnInspectPluginFailure(t *testing.T) {
	dir := fixtureRepo(t)
	handler := InspectToolHandler(dir)
	if _, err := handler(context.Background(), json.RawMessage(`{"plugin":"does-not-exist"}`)); err == nil {
		t.Fatal("expected an error for a plugin that does not exist")
	}
}

func TestInspectToolHandlerRoundTripsValidArguments(t *testing.T) {
	dir := fixtureRepo(t)
	writeManifest(t, dir, "widget-runtime", hostplugin.Manifest{
		APIVersion: hostplugin.ManifestAPIVersion, Name: "widget-runtime", Version: "1.0.0",
		Capabilities: []string{"runtime.start"}, Family: hostplugin.PluginFamilyRuntime,
		Executable: "widget-runtime-bin", Digest: zeroDigest,
	})
	handler := InspectToolHandler(dir)
	out, err := handler(context.Background(), json.RawMessage(`{"plugin":"widget-runtime"}`))
	if err != nil {
		t.Fatalf("handler returned an error: %v", err)
	}
	var detail Detail
	if err := json.Unmarshal([]byte(out), &detail); err != nil {
		t.Fatalf("handler output is not valid JSON: %v (%s)", err, out)
	}
	if detail.Manifest == nil || detail.Manifest.Name != "widget-runtime" {
		t.Fatalf("detail=%+v", detail)
	}
}

func TestInspectToolHandlerRejectsInvalidJSON(t *testing.T) {
	handler := InspectToolHandler(fixtureRepo(t))
	if _, err := handler(context.Background(), json.RawMessage(`{not json`)); err == nil {
		t.Fatal("expected an error for malformed JSON arguments")
	}
}

func TestPluginsResourceHandlerReturnsEveryPluginAsJSON(t *testing.T) {
	dir := fixtureRepo(t)
	writeManifest(t, dir, "widget-runtime", hostplugin.Manifest{
		APIVersion: hostplugin.ManifestAPIVersion, Name: "widget-runtime", Version: "1.0.0",
		Capabilities: []string{"runtime.start"}, Family: hostplugin.PluginFamilyRuntime,
		Executable: "widget-runtime-bin", Digest: zeroDigest,
	})
	handler := PluginsResourceHandler(dir)
	out, mimeType, err := handler(context.Background())
	if err != nil {
		t.Fatalf("handler returned an error: %v", err)
	}
	if mimeType != "application/json" {
		t.Fatalf("mimeType=%q", mimeType)
	}
	var summaries []Summary
	if err := json.Unmarshal([]byte(out), &summaries); err != nil {
		t.Fatalf("handler output is not valid JSON: %v (%s)", err, out)
	}
	if len(summaries) != 1 || summaries[0].Name != "widget-runtime" {
		t.Fatalf("summaries=%+v", summaries)
	}
}

func TestPluginsResourceHandlerSurfacesListErrors(t *testing.T) {
	// A repoRoot with no plugins/ directory at all makes ListPlugins
	// fail (os.ReadDir on a nonexistent path) - the handler must
	// propagate that rather than swallowing it.
	handler := PluginsResourceHandler(filepath.Join(t.TempDir(), "does-not-exist"))
	if _, _, err := handler(context.Background()); err == nil {
		t.Fatal("expected an error for a repo with no plugins/ directory")
	}
}

func TestValidateToolHandlerRoundTripsValidArguments(t *testing.T) {
	dir := fixtureRepo(t)
	mustMkdir(t, filepath.Join(dir, "plugins", "lang-widget"))
	mustWriteFile(t, filepath.Join(dir, "plugins", "lang-widget", "main.go"), "package main\n\nfunc main() {}\n")

	handler := ValidateToolHandler(dir)
	out, err := handler(context.Background(), json.RawMessage(`{"plugin":"lang-widget"}`))
	if err != nil {
		t.Fatalf("handler returned an error: %v", err)
	}
	var report ValidationReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("handler output is not valid JSON: %v (%s)", err, out)
	}
	if report.Plugin != "lang-widget" {
		t.Fatalf("report=%+v", report)
	}
}

func TestValidateToolHandlerRejectsInvalidJSON(t *testing.T) {
	handler := ValidateToolHandler(fixtureRepo(t))
	if _, err := handler(context.Background(), json.RawMessage(`{not json`)); err == nil {
		t.Fatal("expected an error for malformed JSON arguments")
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

func TestCompatibilityCheckFlagsLanguagePluginsWithNetworkPermissions(t *testing.T) {
	cases := []struct {
		name     string
		manifest hostplugin.Manifest
		want     string
	}{
		{
			"language family with network permissions is flagged",
			hostplugin.Manifest{Family: hostplugin.PluginFamilyLanguage, Permissions: hostplugin.PluginPermissions{Network: []string{"kubernetes-api"}}},
			"language-family plugins may not declare network permissions",
		},
		{
			"language family without network permissions is ok",
			hostplugin.Manifest{Family: hostplugin.PluginFamilyLanguage},
			"ok",
		},
		{
			"non-language family with network permissions is ok",
			hostplugin.Manifest{Family: hostplugin.PluginFamilyDeployment, Permissions: hostplugin.PluginPermissions{Network: []string{"kubernetes-api"}}},
			"ok",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := compatibilityCheck(c.manifest); got != c.want {
				t.Errorf("compatibilityCheck(%+v) = %q, want %q", c.manifest, got, c.want)
			}
		})
	}
}

// TestValidateFlagsADigestMismatchAgainstAnExistingExecutable covers
// Validate's report.Valid=false digest-mismatch branch: a manifest
// whose Digest does not match a real, already-built executable's actual
// hash (as opposed to TestCreateScaffoldsARealBuildablePlugin's
// freshly-scaffolded, not-yet-built case, where a digest mismatch is
// expected and does NOT flip Valid to false).
func TestValidateFlagsADigestMismatchAgainstAnExistingExecutable(t *testing.T) {
	dir := fixtureRepo(t)
	writeManifest(t, dir, "widget-runtime", hostplugin.Manifest{
		APIVersion: hostplugin.ManifestAPIVersion, Name: "widget-runtime", Version: "1.0.0",
		Capabilities: []string{"runtime.start"}, Family: hostplugin.PluginFamilyRuntime,
		Executable: "widget-runtime-bin", Digest: zeroDigest,
	})
	mustWriteFile(t, filepath.Join(dir, "plugins", "widget-runtime", "widget-runtime-bin"), "not the real binary")

	report, err := Validate(context.Background(), dir, "widget-runtime")
	if err != nil {
		t.Fatal(err)
	}
	if report.Valid {
		t.Fatalf("expected Valid=false for a digest mismatch against a real executable, report=%+v", report)
	}
	found := false
	for _, issue := range report.Issues {
		if strings.Contains(issue, "digest:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a digest issue to be reported, report=%+v", report)
	}
}

// TestValidateExecutesTestFiles covers the successful test-execution
// branch of Validate - every other Validate test in
// this file uses a fixture with no _test.go files, hitting the "no
// _test.go files found" branch instead.
func TestValidateExecutesTestFiles(t *testing.T) {
	dir := fixtureRepo(t)
	writeManifest(t, dir, "widget-runtime", hostplugin.Manifest{
		APIVersion: hostplugin.ManifestAPIVersion, Name: "widget-runtime", Version: "1.0.0",
		Capabilities: []string{"runtime.start"}, Family: hostplugin.PluginFamilyRuntime,
		Executable: "widget-runtime-bin", Digest: zeroDigest,
	})
	mustWriteFile(t, filepath.Join(dir, "plugins", "widget-runtime", "main_test.go"), "package main\n")

	report, err := Validate(context.Background(), dir, "widget-runtime")
	if err != nil {
		t.Fatal(err)
	}
	if report.Tests != "ok" {
		t.Fatalf("report.Tests=%q", report.Tests)
	}
}

func TestValidateFailsWhenPluginTestsFail(t *testing.T) {
	dir := fixtureRepo(t)
	if _, err := Create(context.Background(), dir, CreateRequest{Name: "failing", Description: "fixture", Capabilities: []string{"detect"}}); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(dir, "plugins", "failing", "cmd", "platform-factory-failing", "main_test.go"), "package main\nimport \"testing\"\nfunc TestFailure(t *testing.T) { t.Fatal(\"boom\") }\n")
	report, err := Validate(context.Background(), dir, "failing")
	if err != nil {
		t.Fatal(err)
	}
	if report.Valid || !strings.HasPrefix(report.Tests, "error:") {
		t.Fatalf("expected failing tests to invalidate plugin: %+v", report)
	}
}

func TestValidateRejectsMarketplaceEntrypointTraversal(t *testing.T) {
	dir := fixtureRepo(t)
	pluginDir := filepath.Join(dir, "plugins", "unsafe")
	mustMkdir(t, pluginDir)
	mustWriteFile(t, filepath.Join(pluginDir, "plugin.yaml"), "api_version: platform-factory.dev/marketplace-manifest/v1\nname: unsafe\nversion: v0.1.0\nentrypoint: ../../outside\n")
	if got := validateMarketplaceManifest(pluginDir, "unsafe"); !strings.Contains(got, "entrypoint") {
		t.Fatalf("expected traversal rejection, got %q", got)
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

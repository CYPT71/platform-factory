package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	hostplugin "github.com/CYPT71/platform-factory/internal/plugin"
)

// ---------------------------------------------------------------------------
// GenericPluginDir
// ---------------------------------------------------------------------------

func TestGenericPluginDirPrefersExplicitValue(t *testing.T) {
	t.Setenv("PLATFORM_FACTORY_PLUGIN_DIR", filepath.Join(t.TempDir(), "from-env"))
	got, err := GenericPluginDir("relative-value")
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs("relative-value")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestGenericPluginDirFallsBackToEnvVar(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "from-env")
	t.Setenv("PLATFORM_FACTORY_PLUGIN_DIR", dir)
	got, err := GenericPluginDir("")
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestGenericPluginDirFallsBackToUserConfigDir(t *testing.T) {
	t.Setenv("PLATFORM_FACTORY_PLUGIN_DIR", "")
	got, err := GenericPluginDir("")
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no user config dir available in this environment: %v", err)
	}
	want := filepath.Join(config, "platform-factory", "plugins")
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

// ---------------------------------------------------------------------------
// InstallPluginBundle
// ---------------------------------------------------------------------------

// newPluginBundleSource writes a self-consistent plugin.json + executable
// pair (the shape InstallPluginBundle expects a caller to have already
// verified) into a fresh temp directory.
func newPluginBundleSource(t *testing.T, name string, executable []byte) (string, hostplugin.Manifest) {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "run"), executable, 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(executable)
	manifest := hostplugin.Manifest{
		APIVersion:   hostplugin.ManifestAPIVersion,
		Name:         name,
		Version:      "v1.0.0",
		Capabilities: []string{"detect"},
		Executable:   "run",
		Digest:       "sha256:" + hex.EncodeToString(digest[:]),
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, hostplugin.ManifestFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return source, manifest
}

func TestInstallPluginBundleInstallsVerifiedBundle(t *testing.T) {
	root := t.TempDir()
	source, manifest := newPluginBundleSource(t, "example-plugin", []byte("plugin payload"))
	if err := InstallPluginBundle(root, source, manifest); err != nil {
		t.Fatalf("InstallPluginBundle: %v", err)
	}
	destination := filepath.Join(root, manifest.Name)
	installed, err := hostplugin.LoadManifest(destination)
	if err != nil {
		t.Fatalf("LoadManifest(destination): %v", err)
	}
	if installed.Name != manifest.Name || installed.Digest != manifest.Digest {
		t.Fatalf("installed=%+v want name/digest matching %+v", installed, manifest)
	}
	if err := installed.VerifyExecutable(destination); err != nil {
		t.Fatalf("installed executable failed verification: %v", err)
	}
	execInfo, err := os.Stat(filepath.Join(destination, manifest.Executable))
	if err != nil {
		t.Fatal(err)
	}
	if execInfo.Mode().Perm() != 0o700 {
		t.Fatalf("executable mode=%v want 0700", execInfo.Mode().Perm())
	}
	manifestInfo, err := os.Stat(filepath.Join(destination, hostplugin.ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	if manifestInfo.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode=%v want 0600", manifestInfo.Mode().Perm())
	}
	// No leftover .install-* staging directory should remain.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".install-") {
			t.Fatalf("leftover staging directory %q was not cleaned up", entry.Name())
		}
	}
}

func TestInstallPluginBundleRejectsAlreadyInstalled(t *testing.T) {
	root := t.TempDir()
	source, manifest := newPluginBundleSource(t, "dup-plugin", []byte("payload"))
	if err := InstallPluginBundle(root, source, manifest); err != nil {
		t.Fatalf("first install: %v", err)
	}
	err := InstallPluginBundle(root, source, manifest)
	if err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Fatalf("err=%v", err)
	}
}

func TestInstallPluginBundleFailsWhenSourceFileMissing(t *testing.T) {
	root := t.TempDir()
	source, manifest := newPluginBundleSource(t, "missing-file-plugin", []byte("payload"))
	if err := os.Remove(filepath.Join(source, manifest.Executable)); err != nil {
		t.Fatal(err)
	}
	if err := InstallPluginBundle(root, source, manifest); err == nil {
		t.Fatal("expected an error when the source executable is missing")
	}
}

func TestInstallPluginBundleDetectsBundleChangedDuringInstall(t *testing.T) {
	root := t.TempDir()
	source, manifest := newPluginBundleSource(t, "changed-plugin", []byte("payload"))
	// Simulate a caller that verified a manifest which no longer matches
	// what's actually sitting in the source directory (e.g. a race with
	// something else rewriting plugin.json between verification and install).
	stale := manifest
	stale.Version = "v2.0.0"
	err := InstallPluginBundle(root, source, stale)
	if err == nil || !strings.Contains(err.Error(), "changed while it was being installed") {
		t.Fatalf("err=%v", err)
	}
}

func TestInstallPluginBundleDetectsTamperedExecutable(t *testing.T) {
	root := t.TempDir()
	source, manifest := newPluginBundleSource(t, "tampered-plugin", []byte("payload"))
	// The manifest the caller already checked still matches plugin.json on
	// disk, but the executable bytes underneath it were swapped afterwards.
	if err := os.WriteFile(filepath.Join(source, manifest.Executable), []byte("swapped payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := InstallPluginBundle(root, source, manifest)
	if err == nil || !strings.Contains(err.Error(), "does not match the manifest pin") {
		t.Fatalf("err=%v", err)
	}
}

func TestInstallPluginBundleFailsWhenRootCannotBeCreated(t *testing.T) {
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(blocker, "plugins") // blocker is a file, not a directory
	source, manifest := newPluginBundleSource(t, "root-plugin", []byte("payload"))
	if err := InstallPluginBundle(root, source, manifest); err == nil {
		t.Fatal("expected MkdirAll to fail when root's parent path is a file")
	}
}

// ---------------------------------------------------------------------------
// prepareSourceFile dispatch
// ---------------------------------------------------------------------------

func TestPrepareSourceFileDispatchesByExtension(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("these branches require a Unix-like host before checking PATH")
	}
	for _, tt := range []struct {
		name          string
		ext           string
		wantErrSubstr string
	}{
		{"python script", ".py", "python3"},
		{"uppercase python extension", ".PY", "python3"},
		{"javascript file", ".js", "node"},
		{"typescript file", ".ts", "node was not found"},
		{"csharp source", ".cs", "dotnet SDK"},
		{"csproj file", ".csproj", "dotnet SDK"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			source := filepath.Join(t.TempDir(), "plugin"+tt.ext)
			if err := os.WriteFile(source, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, cleanup, err := prepareSourceFile(source)
			defer cleanup()
			if err == nil || !strings.Contains(err.Error(), tt.wantErrSubstr) {
				t.Fatalf("ext=%s err=%v", tt.ext, err)
			}
		})
	}
}

func TestPrepareSourceFileDefaultsToPassThroughForUnknownExtension(t *testing.T) {
	source := filepath.Join(t.TempDir(), "plugin.bin")
	if err := os.WriteFile(source, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, cleanup, err := prepareSourceFile(source)
	defer cleanup()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != source {
		t.Fatalf("got=%q want=%q", got, source)
	}
}

// ---------------------------------------------------------------------------
// PrepareSource
// ---------------------------------------------------------------------------

func TestPrepareSourceAcceptsAlreadyBuiltBinary(t *testing.T) {
	source := filepath.Join(t.TempDir(), "already-built")
	if err := os.WriteFile(source, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, cleanup, err := PrepareSource(source)
	defer cleanup()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != source {
		t.Fatalf("got=%q want=%q", got, source)
	}
}

func TestPrepareSourceBuildsGoModuleDirectory(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module pf-test-plugin\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mainGo := "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"built-plugin-ok\") }\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(mainGo), 0o644); err != nil {
		t.Fatal(err)
	}
	binary, cleanup, err := PrepareSource(dir)
	if err != nil {
		cleanup()
		t.Fatalf("PrepareSource: %v", err)
	}
	output, runErr := exec.Command(binary).CombinedOutput()
	if runErr != nil {
		cleanup()
		t.Fatalf("run built plugin: %v: %s", runErr, output)
	}
	if !strings.Contains(string(output), "built-plugin-ok") {
		cleanup()
		t.Fatalf("output=%s", output)
	}
	cleanup()
	if _, err := os.Stat(binary); !os.IsNotExist(err) {
		t.Fatalf("expected cleanup to remove the built binary, stat err=%v", err)
	}
}

func TestPrepareSourceBuildFailureSurfacesCompilerOutput(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module broken-plugin\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() { this is not valid go }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, cleanup, err := PrepareSource(dir)
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "build") {
		t.Fatalf("err=%v", err)
	}
}

func TestPrepareSourceFindsScriptEntrypointInDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("prepareScriptPlugin always errors on windows before checking PATH")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.py"), []byte("print(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // no python3 available
	_, cleanup, err := PrepareSource(dir)
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "python3") {
		t.Fatalf("err=%v", err)
	}
}

func TestPrepareSourceBuildsSingleCsprojInDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Plugin.csproj"), []byte("<Project />"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // no dotnet available
	_, cleanup, err := PrepareSource(dir)
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "dotnet SDK") {
		t.Fatalf("err=%v", err)
	}
}

func TestPrepareSourceRejectsAmbiguousMultipleCsprojFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"A.csproj", "B.csproj"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("<Project />"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, cleanup, err := PrepareSource(dir)
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "no supported plugin entrypoint") {
		t.Fatalf("err=%v", err)
	}
}

func TestPrepareSourceRejectsDirectoryWithNoEntrypoint(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, cleanup, err := PrepareSource(dir)
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "no supported plugin entrypoint") {
		t.Fatalf("err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// prepareTypeScriptPlugin success path
// ---------------------------------------------------------------------------

func TestPrepareTypeScriptPluginStripsTypesAndPreparesScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("prepareScriptPlugin always errors on windows")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	source := filepath.Join(t.TempDir(), "plugin.ts")
	ts := "function greet(name: string): string {\n  return 'hi ' + name;\n}\nconsole.log(greet('world'));\n"
	if err := os.WriteFile(source, []byte(ts), 0o644); err != nil {
		t.Fatal(err)
	}
	prepared, cleanup, err := prepareTypeScriptPlugin(source)
	defer cleanup()
	if err != nil {
		t.Fatalf("prepareTypeScriptPlugin: %v", err)
	}
	content, err := os.ReadFile(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), ": string") {
		t.Fatalf("expected type annotations stripped, got:\n%s", content)
	}
	if !strings.HasPrefix(string(content), "#!/usr/bin/env -S node\n") {
		t.Fatalf("expected a node shebang, got:\n%s", content)
	}
	output, err := exec.Command(prepared).CombinedOutput()
	if err != nil {
		t.Fatalf("run prepared script: %v: %s", err, output)
	}
	if !strings.Contains(string(output), "hi world") {
		t.Fatalf("output=%s", output)
	}
	cleanup()
	if _, err := os.Stat(prepared); !os.IsNotExist(err) {
		t.Fatalf("expected cleanup to remove the generated script, stat err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// buildDotnetPlugin
// ---------------------------------------------------------------------------

func fakeDotnetOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "dotnet")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestBuildDotnetPluginFailsWhenPublishProducesNoExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake dotnet is a shell script")
	}
	fakeDotnetOnPath(t)
	project := filepath.Join(t.TempDir(), "Plugin.csproj")
	if err := os.WriteFile(project, []byte("<Project />"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, cleanup, err := buildDotnetPlugin(project)
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "did not produce executable") {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildDotnetPluginWritesGeneratedProjectForCSharpSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake dotnet is a shell script")
	}
	fakeDotnetOnPath(t)
	source := filepath.Join(t.TempDir(), "Program.cs")
	if err := os.WriteFile(source, []byte("Console.WriteLine(\"hi\");\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, cleanup, err := buildDotnetPlugin(source)
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "did not produce executable") {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildDotnetPluginFailsWhenCSharpSourceMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake dotnet is a shell script")
	}
	fakeDotnetOnPath(t)
	source := filepath.Join(t.TempDir(), "does-not-exist.cs")
	_, cleanup, err := buildDotnetPlugin(source)
	defer cleanup()
	if err == nil {
		t.Fatal("expected an error reading the missing C# source file")
	}
}

// ---------------------------------------------------------------------------
// LocateBuiltinPluginBinary
// ---------------------------------------------------------------------------

func TestLocateBuiltinPluginBinaryFindsSiblingOfRunningBinary(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	fileName := "platform-factory-lang-rust"
	if runtime.GOOS == "windows" {
		fileName += ".exe"
	}
	sibling := filepath.Join(filepath.Dir(self), fileName)
	if err := os.WriteFile(sibling, []byte("fake"), 0o755); err != nil {
		t.Skipf("cannot write next to the running test binary: %v", err)
	}
	defer os.Remove(sibling)
	t.Setenv("PATH", t.TempDir()) // must be found via the binary's own directory, not PATH
	got, err := LocateBuiltinPluginBinary("rust")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got != sibling {
		t.Fatalf("got=%q want=%q", got, sibling)
	}
}

func TestLocateBuiltinPluginBinaryFindsOnPATH(t *testing.T) {
	dir := t.TempDir()
	fileName := "platform-factory-lang-ruby"
	if runtime.GOOS == "windows" {
		fileName += ".exe"
	}
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	got, err := LocateBuiltinPluginBinary("ruby")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got != path {
		t.Fatalf("got=%q want=%q", got, path)
	}
}

func TestLocateBuiltinPluginBinaryNotFoundAnywhere(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := LocateBuiltinPluginBinary("java")
	if err == nil || !strings.Contains(err.Error(), "wasn't found") {
		t.Fatalf("err=%v", err)
	}
}

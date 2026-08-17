package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	hostplugin "github.com/CYPT71/platform-factory/internal/plugin"
	"github.com/CYPT71/platform-factory/sdk/langplugin"
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
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho '{\"match\":false,\"dependencies\":{\"mode\":\"unknown\",\"reason\":\"test\"}}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeRPCPluginBundle(t *testing.T, dir, name string) {
	t.Helper()
	executable := []byte("#!/bin/sh\nexit 0\n")
	digest := sha256.Sum256(executable)
	manifest := hostplugin.Manifest{
		APIVersion: hostplugin.ManifestAPIVersion, Name: name, Version: "1.0.0",
		Capabilities: []string{"monitor"}, Family: hostplugin.PluginFamilyCapability,
		Executable: "bin/plugin", Digest: "sha256:" + hex.EncodeToString(digest[:]),
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, hostplugin.ManifestFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "plugin"), executable, 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestPluginInstallAndRemoveRPCBundle(t *testing.T) {
	withTestPluginDir(t)
	source, registry := t.TempDir(), t.TempDir()
	writeRPCPluginBundle(t, source, "acme-monitor")
	var stdout, stderr bytes.Buffer
	args := []string{"install", "--from", source, "--plugin-dir", registry}
	if code := runPlugin(args, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "unsigned manifest") {
		t.Fatalf("unsigned install code=%d stderr=%s", code, stderr.String())
	}
	stderr.Reset()
	args = append(args, "--allow-unsigned")
	if code := runPlugin(args, &stdout, &stderr); code != 0 {
		t.Fatalf("install code=%d stderr=%s", code, stderr.String())
	}
	installed := filepath.Join(registry, "acme-monitor")
	manifest, err := hostplugin.LoadManifest(installed)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.VerifyExecutable(installed); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := runPlugin([]string{"list", "--plugin-dir", registry}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "acme-monitor installed (RPC, 1.0.0)") {
		t.Fatalf("list code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if code := runPlugin([]string{"remove", "--plugin-dir", registry, "acme-monitor"}, &stdout, &stderr); code != 0 {
		t.Fatalf("remove code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(installed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installed plugin still exists: %v", err)
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
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nimport \"fmt\"\nfunc main(){fmt.Println(`{\"match\":false,\"dependencies\":{\"mode\":\"unknown\",\"reason\":\"test\"}}`)}\n"), 0o644); err != nil {
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
	if !strings.Contains(stderr.String(), "supported plugin entrypoint") {
		t.Fatalf("stderr=%s, want the supported entrypoints", stderr.String())
	}
}

func TestPluginLoadInspectAndUnloadSourceLanguages(t *testing.T) {
	tests := []struct {
		name, tool, filename, source string
	}{
		{"python", "python3", "plugin.py", "import json\nprint(json.dumps({'match': True, 'language': 'acme-python', 'profile': 'python', 'dependencies': {'mode': 'none', 'reason': 'test'}}))\n"},
		{"javascript", "node", "plugin.js", "console.log(JSON.stringify({match:true,language:'acme-js',profile:'node',dependencies:{mode:'none',reason:'test'}}));\n"},
		{"typescript", "node", "plugin.ts", "const result: object = {match:true,language:'acme-ts',profile:'node',dependencies:{mode:'none',reason:'test'}}; console.log(JSON.stringify(result));\n"},
		{"php", "php", "plugin.php", "<?php echo json_encode(['match'=>true,'language'=>'acme-php','profile'=>'php','dependencies'=>['mode'=>'none','reason'=>'test']]);\n"},
		{"csharp", "dotnet", "Plugin.cs", "using System; Console.WriteLine(\"{\\\"match\\\":true,\\\"language\\\":\\\"acme-csharp\\\",\\\"profile\\\":\\\"dotnet\\\",\\\"dependencies\\\":{\\\"mode\\\":\\\"none\\\",\\\"reason\\\":\\\"test\\\"}}\");\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := exec.LookPath(test.tool); err != nil {
				t.Skipf("%s unavailable: %v", test.tool, err)
			}
			withTestPluginDir(t)
			source := filepath.Join(t.TempDir(), test.filename)
			if err := os.WriteFile(source, []byte(test.source), 0o600); err != nil {
				t.Fatal(err)
			}
			pluginName := "source-" + test.name
			var stdout, stderr bytes.Buffer
			if code := runPlugin([]string{"load", "--from", source, pluginName}, &stdout, &stderr); code != 0 {
				t.Fatalf("load code=%d stderr=%s", code, stderr.String())
			}
			binary, err := langplugin.Resolve(pluginName)
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := langplugin.RunInspection(binary, t.TempDir())
			if err != nil || !inspection.Match || !strings.HasPrefix(inspection.Language, "acme-") {
				t.Fatalf("inspection=%+v err=%v", inspection, err)
			}
			stdout.Reset()
			stderr.Reset()
			if code := runPlugin([]string{"unload", pluginName}, &stdout, &stderr); code != 0 {
				t.Fatalf("unload code=%d stderr=%s", code, stderr.String())
			}
			if _, err := langplugin.Resolve(pluginName); err == nil {
				t.Fatal("plugin still resolves after unload")
			}
		})
	}
}

func TestPluginLoadRollsBackInvalidInspectContract(t *testing.T) {
	withTestPluginDir(t)
	source := filepath.Join(t.TempDir(), "broken.py")
	if err := os.WriteFile(source, []byte("print('not json')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runPlugin([]string{"load", "--from", source, "broken"}, &stdout, &stderr); code == 0 {
		t.Fatalf("invalid plugin loaded: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "failed the inspect contract") {
		t.Fatalf("stderr=%s", stderr.String())
	}
	if _, err := langplugin.Resolve("broken"); err == nil {
		t.Fatal("invalid plugin remained installed")
	}
}

func TestIntermediateCreatesPluginThroughLoadedPythonLanguagePlugin(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skipf("python3 unavailable: %v", err)
	}
	withTestPluginDir(t)
	languageBinary := filepath.Join(t.TempDir(), "platform-factory-lang-python")
	if runtime.GOOS == "windows" {
		languageBinary += ".exe"
	}
	build := exec.Command("go", "build", "-o", languageBinary, ".")
	build.Dir = filepath.Join("..", "..", "plugins", "lang-python")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build language plugin: %v\n%s", err, output)
	}
	var stdout, stderr bytes.Buffer
	if code := runPlugin([]string{"load", "--from", languageBinary, "python"}, &stdout, &stderr); code != 0 {
		t.Fatalf("load language plugin: %s", stderr.String())
	}
	generated := filepath.Join(t.TempDir(), "acme-plugin")
	stdout.Reset()
	stderr.Reset()
	if code := runPlugin([]string{"create", "--language", "python", "--output", generated, "acme"}, &stdout, &stderr); code != 0 {
		t.Fatalf("create: code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(generated, "plugin.py")); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runPlugin([]string{"load", "--from", filepath.Join(generated, "plugin.py"), "acme"}, &stdout, &stderr); code != 0 {
		t.Fatalf("load generated plugin: %s", stderr.String())
	}
	if _, err := langplugin.Resolve("acme"); err != nil {
		t.Fatal(err)
	}
	if code := runPlugin([]string{"unload", "acme"}, &stdout, &stderr); code != 0 {
		t.Fatalf("unload generated plugin: %s", stderr.String())
	}
}

func TestIntermediateCreatesPluginsInPreferredLanguage(t *testing.T) {
	tests := []struct{ language, dialect, entry, tool string }{{"node", "js", "plugin.js", "node"}, {"node", "ts", "plugin.ts", "node"}, {"php", "", "plugin.php", "php"}, {"dotnet", "", "Plugin.csproj", "dotnet"}}
	for _, test := range tests {
		t.Run(test.language+test.dialect, func(t *testing.T) {
			if _, err := exec.LookPath(test.tool); err != nil {
				t.Skipf("%s unavailable", test.tool)
			}
			withTestPluginDir(t)
			binary := filepath.Join(t.TempDir(), "platform-factory-lang-"+test.language)
			if runtime.GOOS == "windows" {
				binary += ".exe"
			}
			build := exec.Command("go", "build", "-o", binary, ".")
			build.Dir = filepath.Join("..", "..", "plugins", "lang-"+test.language)
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build: %v\n%s", err, output)
			}
			var stdout, stderr bytes.Buffer
			if code := runPlugin([]string{"load", "--from", binary, test.language}, &stdout, &stderr); code != 0 {
				t.Fatalf("load lang: %s", stderr.String())
			}
			out := filepath.Join(t.TempDir(), "generated")
			args := []string{"create", "--language", test.language, "--output", out}
			if test.dialect != "" {
				args = append(args, "--dialect", test.dialect)
			}
			args = append(args, "acme-"+test.language+test.dialect)
			stdout.Reset()
			stderr.Reset()
			if code := runPlugin(args, &stdout, &stderr); code != 0 {
				t.Fatalf("create: %s", stderr.String())
			}
			entry := filepath.Join(out, test.entry)
			if _, err := os.Stat(entry); err != nil {
				t.Fatal(err)
			}
			name := "generated-" + test.language + test.dialect
			stdout.Reset()
			stderr.Reset()
			if code := runPlugin([]string{"load", "--from", entry, name}, &stdout, &stderr); code != 0 {
				t.Fatalf("load generated: %s", stderr.String())
			}
			if code := runPlugin([]string{"unload", name}, &stdout, &stderr); code != 0 {
				t.Fatalf("unload: %s", stderr.String())
			}
		})
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

// loadFakeScaffoldPlugin registers name as a loaded language plugin
// whose binary is a script recording the arguments it was invoked with
// to argsFile and exiting with exitCode - enough to exercise
// runPluginCreate's own "scaffold" argv construction and success/
// failure handling without a real language plugin.
func loadFakeScaffoldPlugin(t *testing.T, name, argsFile string, exitCode int) {
	t.Helper()
	t.Setenv("PLATFORM_FACTORY_LANG_PLUGIN_DIR", t.TempDir())
	source := filepath.Join(t.TempDir(), "plugin-binary")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" > %q\nexit %d\n", argsFile, exitCode)
	if err := os.WriteFile(source, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := langplugin.Load(name, source); err != nil {
		t.Fatal(err)
	}
}

func TestRunPluginCreateUsageErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runPluginCreate(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("no args: code=%d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runPluginCreate([]string{"--language", "python"}, &stdout, &stderr); code != 2 {
		t.Fatalf("missing name: code=%d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runPluginCreate([]string{"myplugin"}, &stdout, &stderr); code != 2 {
		t.Fatalf("missing --language: code=%d", code)
	}
}

func TestRunPluginCreateRequiresALoadedPlugin(t *testing.T) {
	t.Setenv("PLATFORM_FACTORY_LANG_PLUGIN_DIR", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := runPluginCreate([]string{"--language", "python", "myplugin"}, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunPluginCreateSucceeds(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	loadFakeScaffoldPlugin(t, "python", argsFile, 0)
	var stdout, stderr bytes.Buffer
	code := runPluginCreate([]string{"--language", "python", "--dialect", "ts", "--output", "/out", "myplugin"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	recorded, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(recorded))
	want := "scaffold --name myplugin --output /out --dialect ts"
	if got != want {
		t.Fatalf("recorded args = %q, want %q", got, want)
	}
}

func TestRunPluginCreateSurfacesScaffoldFailure(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	loadFakeScaffoldPlugin(t, "python", argsFile, 1)
	var stdout, stderr bytes.Buffer
	code := runPluginCreate([]string{"--language", "python", "myplugin"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stderr.String(), "cannot scaffold this plugin") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestLocateBuiltinPluginBinaryRejectsUnknownLanguage(t *testing.T) {
	if _, err := locateBuiltinPluginBinary("cobol"); err == nil {
		t.Fatal("expected an error for a non-built-in language")
	}
}

func TestLocateBuiltinPluginBinaryFindsItOnPATH(t *testing.T) {
	dir := t.TempDir()
	name := "platform-factory-lang-ruby"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(dir, name)
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	got, err := locateBuiltinPluginBinary("ruby")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resolved, evalErr := filepath.EvalSymlinks(got)
	if evalErr != nil {
		resolved = got
	}
	wantResolved, _ := filepath.EvalSymlinks(binary)
	if resolved != wantResolved {
		t.Fatalf("got=%q want=%q", got, binary)
	}
}

func TestLocateBuiltinPluginBinaryNotFoundAnywhere(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := locateBuiltinPluginBinary("php"); err == nil {
		t.Fatal("expected an error when the binary is not next to the CLI or on PATH")
	}
}

func TestPrepareTypeScriptPluginRequiresNodeOnPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, cleanup, err := prepareTypeScriptPlugin(filepath.Join(t.TempDir(), "plugin.ts"))
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "node was not found on PATH") {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildDotnetPluginRequiresDotnetOnPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, cleanup, err := buildDotnetPlugin(filepath.Join(t.TempDir(), "Plugin.cs"))
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "dotnet SDK was not found on PATH") {
		t.Fatalf("err=%v", err)
	}
}

func TestPrepareScriptPluginRequiresTheInterpreterOnPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("prepareScriptPlugin always errors on windows before checking PATH")
	}
	t.Setenv("PATH", t.TempDir())
	_, cleanup, err := prepareScriptPlugin(filepath.Join(t.TempDir(), "plugin.py"), "python3", nil)
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "was not found on PATH") {
		t.Fatalf("err=%v", err)
	}
}

func TestPrepareScriptPluginSurfacesInterpreterProbeFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("prepareScriptPlugin always errors on windows before checking PATH")
	}
	dir := t.TempDir()
	interpreter := filepath.Join(dir, "brokenpy")
	if err := os.WriteFile(interpreter, []byte("#!/bin/sh\necho probe failed >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	source := filepath.Join(t.TempDir(), "plugin.py")
	if err := os.WriteFile(source, []byte("print(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, cleanup, err := prepareScriptPlugin(source, "brokenpy", []string{"--strict"})
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "cannot execute this plugin source") {
		t.Fatalf("err=%v", err)
	}
}

func TestPrepareScriptPluginStripsShebangAndWrapsInterpreter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("prepareScriptPlugin always errors on windows")
	}
	dir := t.TempDir()
	interpreter := filepath.Join(dir, "fakepy")
	if err := os.WriteFile(interpreter, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	source := filepath.Join(t.TempDir(), "plugin.py")
	if err := os.WriteFile(source, []byte("#!/usr/bin/env python3\nprint(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prepared, cleanup, err := prepareScriptPlugin(source, "fakepy", nil)
	defer cleanup()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	content, readErr := os.ReadFile(prepared)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(content), "env python3") {
		t.Fatalf("expected the original shebang to be stripped, got:\n%s", content)
	}
	if !strings.HasPrefix(string(content), "#!/usr/bin/env -S fakepy\n") {
		t.Fatalf("expected the new interpreter shebang, got:\n%s", content)
	}
	if !strings.Contains(string(content), "print(1)") {
		t.Fatalf("expected the original source body preserved, got:\n%s", content)
	}
	info, statErr := os.Stat(prepared)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("expected the prepared script to be executable, mode=%v", info.Mode())
	}
}

func TestRunPluginInstallErrorPaths(t *testing.T) {
	registry := t.TempDir()
	var stdout, stderr bytes.Buffer

	if code := runPluginInstall([]string{"--plugin-dir", registry}, &stdout, &stderr); code != 2 {
		t.Fatalf("missing --from: code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runPluginInstall([]string{"extra", "--from", t.TempDir(), "--plugin-dir", registry}, &stdout, &stderr); code != 2 {
		t.Fatalf("unexpected positional arg: code=%d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runPluginInstall([]string{"--from", filepath.Join(t.TempDir(), "missing"), "--plugin-dir", registry}, &stdout, &stderr); code != 1 {
		t.Fatalf("missing source dir: code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "platform-factory plugin install:") {
		t.Fatalf("missing source dir stderr=%q", stderr.String())
	}

	source := t.TempDir()
	writeRPCPluginBundle(t, source, "acme-monitor")
	stdout.Reset()
	stderr.Reset()
	if code := runPluginInstall([]string{"--from", source, "--plugin-dir", registry, "--allow-unsigned"}, &stdout, &stderr); code != 0 {
		t.Fatalf("fresh install: code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runPluginInstall([]string{"--from", source, "--plugin-dir", registry, "--allow-unsigned"}, &stdout, &stderr); code != 1 {
		t.Fatalf("duplicate install: code=%d", code)
	}
	if !strings.Contains(stderr.String(), "already installed") {
		t.Fatalf("duplicate install stderr=%q", stderr.String())
	}
}

func TestRunPluginRemoveErrorPaths(t *testing.T) {
	registry := t.TempDir()
	var stdout, stderr bytes.Buffer

	if code := runPluginRemove(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("no args: code=%d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runPluginRemove([]string{"a", "b"}, &stdout, &stderr); code != 2 {
		t.Fatalf("too many args: code=%d", code)
	}
	for _, name := range []string{"../escape", "with/slash", "with\\backslash", ".", ".."} {
		stdout.Reset()
		stderr.Reset()
		if code := runPluginRemove([]string{"--plugin-dir", registry, name}, &stdout, &stderr); code != 2 {
			t.Fatalf("invalid name %q: code=%d stderr=%s", name, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "invalid plugin name") {
			t.Fatalf("invalid name %q stderr=%q", name, stderr.String())
		}
	}
	stdout.Reset()
	stderr.Reset()
	if code := runPluginRemove([]string{"--plugin-dir", registry, "never-installed"}, &stdout, &stderr); code != 1 {
		t.Fatalf("unmanaged plugin: code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "refuse to remove") {
		t.Fatalf("unmanaged plugin stderr=%q", stderr.String())
	}
}

func TestGenericPluginDir(t *testing.T) {
	explicit := t.TempDir()
	got, err := genericPluginDir(explicit)
	if err != nil {
		t.Fatalf("explicit dir: %v", err)
	}
	want, _ := filepath.Abs(explicit)
	if got != want {
		t.Fatalf("explicit dir: got %q want %q", got, want)
	}

	envDir := t.TempDir()
	t.Setenv("PLATFORM_FACTORY_PLUGIN_DIR", envDir)
	got, err = genericPluginDir("")
	if err != nil {
		t.Fatalf("env dir: %v", err)
	}
	want, _ = filepath.Abs(envDir)
	if got != want {
		t.Fatalf("env dir: got %q want %q", got, want)
	}

	t.Setenv("PLATFORM_FACTORY_PLUGIN_DIR", "")
	got, err = genericPluginDir("")
	if err != nil {
		t.Fatalf("default dir: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join("platform-factory", "plugins")) {
		t.Fatalf("default dir: got %q", got)
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

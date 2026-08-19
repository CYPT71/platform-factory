// Package plugin is the application-layer service behind `pf plugin`'s
// self-contained business rules: turning a --from source (an already-
// built binary, a Go module, a Python/JavaScript/TypeScript/PHP script,
// or a C# project) into a loadable plugin binary, locating a built-in
// language plugin's binary, resolving the generic RPC-bundle plugin
// directory, and installing a verified bundle into it. cmd/platform-
// factory/plugin.go still owns everything that calls sdk/langplugin
// directly (Load/Unload/List/Resolve/RunInspection) - this package has
// no sdk/ dependency, so that orchestration can't live here - plus flag
// parsing and output formatting; the source-preparation and generic-
// bundle logic that never touches sdk/langplugin lives here, where it
// can be tested without going through the CLI at all.
package plugin

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	hostplugin "github.com/CYPT71/platform-factory/internal/plugin"
)

// BuiltinLanguages is every language platform-factory ships its own
// plugin for.
var BuiltinLanguages = []string{"python", "node", "ruby", "php", "java", "dotnet", "rust"}

func isBuiltinLanguage(name string) bool {
	for _, l := range BuiltinLanguages {
		if l == name {
			return true
		}
	}
	return false
}

// BuiltinLanguageList renders BuiltinLanguages as a comma-separated list
// for usage text.
func BuiltinLanguageList() string {
	out := ""
	for i, l := range BuiltinLanguages {
		if i > 0 {
			out += ", "
		}
		out += l
	}
	return out
}

// GenericPluginDir resolves the managed directory generic (RPC-bundle)
// plugins install into: value if given, otherwise
// PLATFORM_FACTORY_PLUGIN_DIR, otherwise a "platform-factory/plugins"
// subdirectory of the user's config directory.
func GenericPluginDir(value string) (string, error) {
	if value != "" {
		return filepath.Abs(value)
	}
	if value = os.Getenv("PLATFORM_FACTORY_PLUGIN_DIR"); value != "" {
		return filepath.Abs(value)
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(config, "platform-factory", "plugins"), nil
}

// InstallPluginBundle installs a verified generic plugin bundle from
// source into root, atomically and only after re-verifying the copied
// bundle matches the manifest the caller already checked.
func InstallPluginBundle(root, source string, manifest hostplugin.Manifest) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	destination := filepath.Join(root, manifest.Name)
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("plugin %q is already installed", manifest.Name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.MkdirTemp(root, ".install-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	for _, name := range []string{hostplugin.ManifestFileName, manifest.Executable} {
		data, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if name == manifest.Executable {
			mode = 0o700
		}
		target := filepath.Join(temporary, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, mode); err != nil {
			return err
		}
	}
	installed, err := hostplugin.LoadManifest(temporary)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(installed, manifest) {
		return errors.New("plugin bundle changed while it was being installed")
	}
	if err := installed.VerifyExecutable(temporary); err != nil {
		return err
	}
	return os.Rename(temporary, destination)
}

// PrepareSource makes --from usable whether it names an already-built
// binary or a directory holding a plugin's Go module source: a
// pre-built binary is returned as-is (a no-op cleanup); a directory
// containing a go.mod is built with `go build -o <temp> .` and the
// resulting temporary binary is returned, with cleanup removing it once
// the caller has copied it into the managed plugin directory. This is
// what lets `pf plugin load --from ./my-plugin-source my-plugin` work
// without a separate manual build step first.
func PrepareSource(from string) (path string, cleanup func(), err error) {
	noop := func() {}
	info, err := os.Stat(from)
	if err != nil {
		return "", noop, fmt.Errorf("read %s: %w", from, err)
	}
	if info.Mode().IsRegular() {
		return prepareSourceFile(from)
	}
	if !info.IsDir() {
		return "", noop, fmt.Errorf("%s is neither a file nor a directory", from)
	}
	if _, err := os.Stat(filepath.Join(from, "go.mod")); err == nil {
		return buildGoPlugin(from)
	}
	for _, candidate := range []string{"plugin.py", "plugin.js", "plugin.mjs", "plugin.cjs", "plugin.ts", "plugin.php"} {
		path := filepath.Join(from, candidate)
		if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
			return prepareSourceFile(path)
		}
	}
	projects, _ := filepath.Glob(filepath.Join(from, "*.csproj"))
	if len(projects) == 1 {
		return buildDotnetPlugin(projects[0])
	}
	return "", noop, fmt.Errorf("%s has no supported plugin entrypoint (go.mod, plugin.py, plugin.js, plugin.ts, plugin.php, or one .csproj)", from)
}

func prepareSourceFile(from string) (string, func(), error) {
	switch strings.ToLower(filepath.Ext(from)) {
	case ".py":
		return prepareScriptPlugin(from, "python3", nil)
	case ".js", ".mjs", ".cjs":
		return prepareScriptPlugin(from, "node", nil)
	case ".ts":
		return prepareTypeScriptPlugin(from)
	case ".php":
		return prepareScriptPlugin(from, "php", nil)
	case ".cs", ".csproj":
		return buildDotnetPlugin(from)
	default:
		return from, func() {}, nil
	}
}

func prepareTypeScriptPlugin(source string) (string, func(), error) {
	if _, err := exec.LookPath("node"); err != nil {
		return "", func() {}, errors.New("load TypeScript plugin: node was not found on PATH")
	}
	transform := `const fs=require('node:fs');const {stripTypeScriptTypes}=require('node:module');process.stdout.write(stripTypeScriptTypes(fs.readFileSync(process.argv[1],'utf8'),{mode:'strip'}));`
	cmd := exec.Command("node", "--no-warnings", "-e", transform, source)
	javascript, err := cmd.CombinedOutput()
	if err != nil {
		return "", func() {}, fmt.Errorf("transform TypeScript plugin %s: %w: %s", source, err, strings.TrimSpace(string(javascript)))
	}
	tmp, err := os.CreateTemp("", "platform-factory-typescript-plugin-*.js")
	if err != nil {
		return "", func() {}, err
	}
	generated := tmp.Name()
	if _, writeErr := tmp.Write(javascript); writeErr == nil {
		err = tmp.Close()
	} else {
		err = writeErr
		_ = tmp.Close()
	}
	if err != nil {
		_ = os.Remove(generated)
		return "", func() {}, err
	}
	prepared, cleanupPrepared, err := prepareScriptPlugin(generated, "node", nil)
	cleanup := func() {
		cleanupPrepared()
		_ = os.Remove(generated)
	}
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	return prepared, cleanup, nil
}

func prepareScriptPlugin(source, interpreter string, interpreterArgs []string) (string, func(), error) {
	if runtime.GOOS == "windows" {
		return "", func() {}, errors.New("source-script plugin loading currently requires a Unix-like host; provide a Windows executable with --from")
	}
	if _, err := exec.LookPath(interpreter); err != nil {
		return "", func() {}, fmt.Errorf("load %s plugin: required interpreter %q was not found on PATH", filepath.Ext(source), interpreter)
	}
	if len(interpreterArgs) > 0 {
		probe := exec.Command(interpreter, append(interpreterArgs, "-e", "")...)
		if output, err := probe.CombinedOutput(); err != nil {
			return "", func() {}, fmt.Errorf("%s cannot execute this plugin source: %w: %s", interpreter, err, strings.TrimSpace(string(output)))
		}
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		return "", func() {}, err
	}
	if bytes.HasPrefix(raw, []byte("#!")) {
		if newline := bytes.IndexByte(raw, '\n'); newline >= 0 {
			raw = raw[newline+1:]
		}
	}
	tmp, err := os.CreateTemp("", "platform-factory-script-plugin-*")
	if err != nil {
		return "", func() {}, err
	}
	args := ""
	if len(interpreterArgs) > 0 {
		args = " " + strings.Join(interpreterArgs, " ")
	}
	if _, err := fmt.Fprintf(tmp, "#!/usr/bin/env -S %s%s\n", interpreter, args); err == nil {
		_, err = tmp.Write(raw)
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o755)
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	return tmp.Name(), cleanup, nil
}

func buildGoPlugin(from string) (string, func(), error) {
	noop := func() {}
	tmp, err := os.CreateTemp("", "platform-factory-plugin-build-*")
	if err != nil {
		return "", noop, fmt.Errorf("create temporary build output: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpPath) // go build creates it itself; a stale empty file would only get in the way
	cleanup := func() { _ = os.Remove(tmpPath) }

	cmd := exec.Command("go", "build", "-o", tmpPath, ".")
	cmd.Dir = from
	cmd.Env = append(os.Environ(), "GOWORK=off") // from's go.mod is its own module, never this repo's workspace
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", cleanup, fmt.Errorf("build %s: %w\n%s", from, err, stderr.String())
	}
	return tmpPath, cleanup, nil
}

func buildDotnetPlugin(from string) (string, func(), error) {
	if _, err := exec.LookPath("dotnet"); err != nil {
		return "", func() {}, errors.New("load C# plugin: dotnet SDK was not found on PATH")
	}
	workspace, err := os.MkdirTemp("", "platform-factory-dotnet-plugin-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(workspace) }
	project := from
	if strings.EqualFold(filepath.Ext(from), ".cs") {
		project = filepath.Join(workspace, "Plugin.csproj")
		projectFile := []byte("<Project Sdk=\"Microsoft.NET.Sdk\"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net8.0</TargetFramework><ImplicitUsings>enable</ImplicitUsings><Nullable>enable</Nullable></PropertyGroup></Project>\n")
		if err := os.WriteFile(project, projectFile, 0o600); err != nil {
			cleanup()
			return "", func() {}, err
		}
		raw, readErr := os.ReadFile(from)
		if readErr != nil {
			cleanup()
			return "", func() {}, readErr
		}
		if err := os.WriteFile(filepath.Join(workspace, "Program.cs"), raw, 0o600); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	outputDir := filepath.Join(workspace, "publish")
	cmd := exec.Command("dotnet", "publish", project, "--configuration", "Release", "--output", outputDir, "--nologo", "--self-contained", "true", "-p:PublishSingleFile=true")
	var buildOutput bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buildOutput, &buildOutput
	if err := cmd.Run(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("build C# plugin %s: %w\n%s", from, err, buildOutput.String())
	}
	base := strings.TrimSuffix(filepath.Base(project), filepath.Ext(project))
	executable := filepath.Join(outputDir, base)
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	if info, err := os.Stat(executable); err != nil || !info.Mode().IsRegular() {
		cleanup()
		return "", func() {}, fmt.Errorf("C# build did not produce executable %s", executable)
	}
	return executable, cleanup, nil
}

// LocateBuiltinPluginBinary finds a ready-to-load binary for one of
// platform-factory's own built-in languages, without the caller having
// to know it's an executable named platform-factory-lang-<name>, let
// alone where it lives. It looks first next to the running
// platform-factory binary (where an install that bundled language
// plugins would put it), then on $PATH (for a from-source build where
// each plugin module was built separately).
func LocateBuiltinPluginBinary(name string) (string, error) {
	if !isBuiltinLanguage(name) {
		return "", fmt.Errorf("%q isn't one of platform-factory's built-in languages (%s) - pass --from PATH to load a plugin binary of your own", name, BuiltinLanguageList())
	}
	fileName := "platform-factory-lang-" + name
	if runtime.GOOS == "windows" {
		fileName += ".exe"
	}
	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), fileName)
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	if path, err := exec.LookPath(fileName); err == nil {
		return path, nil
	}
	return "", fmt.Errorf(
		"%s wasn't found next to this platform-factory binary or on PATH - build it first with `go build -o %s ./plugins/lang-%s`, or pass --from PATH",
		fileName, fileName, name)
}

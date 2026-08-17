package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"

	hostplugin "github.com/CYPT71/platform-factory/internal/plugin"
	"github.com/CYPT71/platform-factory/sdk/langplugin"
)

var builtinLanguages = []string{"python", "node", "ruby", "php", "java", "dotnet", "rust"}

func isBuiltinLanguage(name string) bool {
	for _, l := range builtinLanguages {
		if l == name {
			return true
		}
	}
	return false
}

func runPlugin(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		printPluginUsage(stderr)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		printPluginUsage(stdout)
		return 0
	case "load":
		return runPluginLoad(args[1:], stdout, stderr)
	case "install":
		return runPluginInstall(args[1:], stdout, stderr)
	case "create":
		return runPluginCreate(args[1:], stdout, stderr)
	case "unload":
		return runPluginUnload(args[1:], stdout, stderr)
	case "remove":
		return runPluginRemove(args[1:], stdout, stderr)
	case "list":
		return runPluginList(args[1:], stdout, stderr)
	case "provision-runtime":
		return runPluginProvisionRuntime(context.Background(), args[1:], stdout, stderr, executeProjectCommand)
	default:
		fmt.Fprintf(stderr, "platform-factory plugin: unknown subcommand %q\n\n", args[0])
		printPluginUsage(stderr)
		return 2
	}
}

func printPluginUsage(output io.Writer) {
	fmt.Fprintln(output, `platform-factory plugin — turn language support on or off, no build tools or PATH required

Usage:
  platform-factory plugin load [--from PATH] NAME
  platform-factory plugin install --from DIR [--key PUBLIC.pem]
  platform-factory plugin create --language LANGUAGE [--dialect js|ts] [--output DIR] NAME
  platform-factory plugin unload NAME
  platform-factory plugin remove [--plugin-dir DIR] NAME
  platform-factory plugin list [--plugin-dir DIR]
  platform-factory plugin provision-runtime --language LANGUAGE --image IMAGE@sha256:DIGEST [--dir DIR] [--arch amd64]

provision-runtime pulls a digest-pinned base image via the native OCI
registry client (never docker/podman), asks the named language's own
plugin to resolve a real Linux interpreter and its shared library
dependencies out of it, and records the result as pf.yaml's runtime/args/
include fields - the fix for "pf build" refusing an interpreted-language
project with no runtime provider selected. Requires the plugin be loaded
and support the "runtime" subcommand (currently: python).

NAME is one of `+builtinLanguageList()+` for a language platform-factory
already ships a plugin for, or any name you choose together with
--from PATH for a plugin of your own. --from PATH may point at an
already-built binary, a Python/JavaScript/TypeScript/PHP source file, a C#
source/project, or a directory containing one of those plugin entrypoints.
Go and C# sources are compiled before loading; script sources are installed
with their required interpreter entrypoint.

load/unload manage language-command plugins. install/remove manage generic
framed-RPC bundles containing plugin.json and its digest-pinned executable.

Examples:
  platform-factory plugin load python
  platform-factory plugin load --from ./my-plugin-binary my-plugin
  platform-factory plugin load --from ./my-plugin-source my-plugin
  platform-factory plugin load --from ./plugin.py acme-python
  platform-factory plugin install --from ./acme-runtime --key ./publisher.pem
  platform-factory plugin load --from ./plugin.csproj acme-dotnet
  platform-factory plugin create --language python --output ./acme-plugin acme
  platform-factory plugin list
  platform-factory plugin unload python`)
}

func genericPluginDir(value string) (string, error) {
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

func runPluginInstall(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plugin install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	from := flags.String("from", "", "bundle directory containing plugin.json")
	dir := flags.String("plugin-dir", "", "managed generic plugin directory")
	allowUnsigned := flags.Bool("allow-unsigned", false, "accept an unsigned manifest; digest pinning remains required")
	var keyFiles repeatedFlag
	flags.Var(&keyFiles, "key", "trusted Ed25519 publisher public key; repeatable")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *from == "" {
		fmt.Fprintln(stderr, "usage: platform-factory plugin install --from DIR [--key PUBLIC.pem]")
		return 2
	}
	manifest, err := hostplugin.LoadManifest(*from)
	if err == nil {
		err = manifest.VerifyExecutable(*from)
	}
	if err == nil && manifest.Signature == nil && !*allowUnsigned {
		err = errors.New("unsigned manifest; pass --allow-unsigned only for local development")
	}
	if err == nil && manifest.Signature != nil {
		keys := make([]ed25519.PublicKey, 0, len(keyFiles))
		for _, filename := range keyFiles {
			key, loadErr := hostplugin.LoadPublicKey(filename)
			if loadErr != nil {
				err = loadErr
				break
			}
			keys = append(keys, key)
		}
		if err == nil {
			err = manifest.VerifySignature(keys)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin install: %v\n", err)
		return 1
	}
	root, err := genericPluginDir(*dir)
	if err == nil {
		err = installPluginBundle(root, *from, manifest)
	}
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin install: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "installed %s (%s)\n", manifest.Name, filepath.Join(root, manifest.Name))
	return 0
}

func installPluginBundle(root, source string, manifest hostplugin.Manifest) error {
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

func runPluginRemove(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plugin remove", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("plugin-dir", "", "managed generic plugin directory")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: platform-factory plugin remove [--plugin-dir DIR] NAME")
		return 2
	}
	name := flags.Arg(0)
	if strings.ContainsAny(name, `/\\`) || name == "." || name == ".." {
		fmt.Fprintln(stderr, "platform-factory plugin remove: invalid plugin name")
		return 2
	}
	root, err := genericPluginDir(*dir)
	if err == nil {
		target := filepath.Join(root, name)
		manifest, loadErr := hostplugin.LoadManifest(target)
		if loadErr != nil {
			err = fmt.Errorf("refuse to remove unmanaged or invalid plugin %q: %w", name, loadErr)
		} else if manifest.Name != name {
			err = fmt.Errorf("refuse to remove plugin %q: manifest names %q", name, manifest.Name)
		} else {
			err = os.RemoveAll(target)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin remove: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "removed %s\n", name)
	return 0
}

func runPluginCreate(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plugin create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	language := flags.String("language", "", "loaded language plugin used to create the new plugin")
	dialect := flags.String("dialect", "", "language variant such as js or ts")
	output := flags.String("output", ".", "empty output directory")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 || *language == "" {
		fmt.Fprintln(stderr, "usage: platform-factory plugin create --language LANGUAGE [--dialect js|ts] [--output DIR] NAME")
		return 2
	}
	binary, err := langplugin.Resolve(*language)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin create: %v\n", err)
		return 2
	}
	commandArgs := []string{"scaffold", "--name", flags.Arg(0), "--output", *output}
	if *dialect != "" {
		commandArgs = append(commandArgs, "--dialect", *dialect)
	}
	cmd := exec.Command(binary, commandArgs...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin create: language plugin %s cannot scaffold this plugin: %v\n", *language, err)
		return 1
	}
	return 0
}

func builtinLanguageList() string {
	out := ""
	for i, l := range builtinLanguages {
		if i > 0 {
			out += ", "
		}
		out += l
	}
	return out
}

func runPluginLoad(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plugin load", flag.ContinueOnError)
	flags.SetOutput(stderr)
	from := flags.String("from", "", "path to a plugin binary to load (required unless NAME is a built-in language: "+builtinLanguageList()+")")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: platform-factory plugin load [--from PATH] NAME")
		return 2
	}
	name := flags.Arg(0)
	source := *from
	if source == "" {
		found, err := locateBuiltinPluginBinary(name)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory plugin load: %v\n", err)
			return 1
		}
		source = found
	} else {
		resolved, cleanup, err := prepareSource(source)
		defer cleanup()
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory plugin load: %v\n", err)
			return 1
		}
		source = resolved
	}
	installedPath, err := langplugin.Load(name, source)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin load: %v\n", err)
		return 1
	}
	probeRoot, probeErr := os.MkdirTemp("", "platform-factory-plugin-probe-*")
	if probeErr == nil {
		_, probeErr = langplugin.RunInspection(installedPath, probeRoot)
		_ = os.RemoveAll(probeRoot)
	}
	if probeErr != nil {
		_ = langplugin.Unload(name)
		fmt.Fprintf(stderr, "platform-factory plugin load: plugin failed the inspect contract and was not loaded: %v\n", probeErr)
		return 1
	}
	fmt.Fprintf(stdout, "loaded %s (%s)\n", name, installedPath)
	return 0
}

// prepareSource makes --from usable whether it names an already-built
// binary or a directory holding a plugin's Go module source: a
// pre-built binary is returned as-is (a no-op cleanup); a directory
// containing a go.mod is built with `go build -o <temp> .` and the
// resulting temporary binary is returned, with cleanup removing it once
// Load has copied it into the managed plugin directory. This is what
// lets `pf plugin load --from ./my-plugin-source my-plugin` work
// without a separate manual build step first.
func prepareSource(from string) (path string, cleanup func(), err error) {
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

// locateBuiltinPluginBinary finds a ready-to-load binary for one of
// platform-factory's own built-in languages, without the caller having
// to know it's an executable named platform-factory-lang-<name>, let
// alone where it lives. It looks first next to the running
// platform-factory binary (where an install that bundled language
// plugins would put it), then on $PATH (for a from-source build where
// each plugin module was built separately).
func locateBuiltinPluginBinary(name string) (string, error) {
	if !isBuiltinLanguage(name) {
		return "", fmt.Errorf("%q isn't one of platform-factory's built-in languages (%s) - pass --from PATH to load a plugin binary of your own", name, builtinLanguageList())
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

func runPluginUnload(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plugin unload", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: platform-factory plugin unload NAME")
		return 2
	}
	name := flags.Arg(0)
	if err := langplugin.Unload(name); err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin unload: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "unloaded %s\n", name)
	return 0
}

func runPluginList(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plugin list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("plugin-dir", "", "managed generic plugin directory")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	loaded, err := langplugin.List()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin list: %v\n", err)
		return 1
	}
	loadedSet := make(map[string]bool, len(loaded))
	for _, name := range loaded {
		loadedSet[name] = true
	}
	for _, name := range builtinLanguages {
		status := "not loaded"
		if loadedSet[name] {
			status = "loaded"
		}
		fmt.Fprintf(stdout, "%-10s %s\n", name, status)
		delete(loadedSet, name)
	}
	var custom []string
	for name := range loadedSet {
		custom = append(custom, name)
	}
	sort.Strings(custom)
	for _, name := range custom {
		fmt.Fprintf(stdout, "%-10s loaded (custom)\n", name)
	}
	root, err := genericPluginDir(*dir)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin list: %v\n", err)
		return 1
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return 0
	} else if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin list: %v\n", err)
		return 1
	}
	installed, err := hostplugin.Discover(root)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin list: %v\n", err)
		return 1
	}
	for _, plugin := range installed {
		fmt.Fprintf(stdout, "%-10s installed (RPC, %s)\n", plugin.Manifest.Name, plugin.Manifest.Version)
	}
	return 0
}

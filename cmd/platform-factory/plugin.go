package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	pluginapp "github.com/CYPT71/platform-factory/internal/app/plugin"
	hostplugin "github.com/CYPT71/platform-factory/internal/plugin"
	"github.com/CYPT71/platform-factory/sdk/langplugin"
)

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
		return runPluginProvisionRuntime(context.Background(), args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "platform-factory plugin: unknown subcommand %q\n\n", args[0])
		printPluginUsage(stderr)
		return 2
	}
}

func printPluginUsage(output io.Writer) {
	fmt.Fprintln(output, `platform-factory plugin — turn language support on or off, no build tools or PATH required

Usage:
  platform-factory plugin load [--from PATH] [--format text|json] NAME
  platform-factory plugin install --from DIR [--key PUBLIC.pem] [--format text|json]
  platform-factory plugin create --language LANGUAGE [--dialect js|ts] [--output DIR] [--format text|json] NAME
  platform-factory plugin unload [--format text|json] NAME
  platform-factory plugin remove [--plugin-dir DIR] [--format text|json] NAME
  platform-factory plugin list [--plugin-dir DIR] [--format text|json]
  platform-factory plugin provision-runtime --language LANGUAGE --image IMAGE@sha256:DIGEST [--dir DIR] [--arch amd64]

provision-runtime pulls a digest-pinned base image via the native OCI
registry client (never docker/podman), asks the named language's own
plugin to resolve a real Linux interpreter and its shared library
dependencies out of it, and records the result as pf.yaml's runtime/args/
include fields - the fix for "pf build" refusing an interpreted-language
project with no runtime provider selected. Requires the plugin be loaded
and support the "runtime" subcommand (currently: python).

NAME is one of `+pluginapp.BuiltinLanguageList()+` for a language platform-factory
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

func runPluginInstall(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plugin install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	from := flags.String("from", "", "bundle directory containing plugin.json")
	dir := flags.String("plugin-dir", "", "managed generic plugin directory")
	allowUnsigned := flags.Bool("allow-unsigned", false, "accept an unsigned manifest; digest pinning remains required")
	format := flags.String("format", "text", "output format: text or json")
	var keyFiles repeatedFlag
	flags.Var(&keyFiles, "key", "trusted Ed25519 publisher public key; repeatable")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *from == "" || !validOutputFormat(*format) {
		fmt.Fprintln(stderr, "usage: platform-factory plugin install --from DIR [--key PUBLIC.pem] [--format text|json]")
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
	root, err := pluginapp.GenericPluginDir(*dir)
	if err == nil {
		err = pluginapp.InstallPluginBundle(root, *from, manifest)
	}
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin install: %v\n", err)
		return 1
	}
	target := filepath.Join(root, manifest.Name)
	if *format == "json" {
		return writePluginMutationJSON(stdout, stderr, "install", manifest.Name, "installed", target)
	}
	fmt.Fprintf(stdout, "installed %s (%s)\n", manifest.Name, target)
	return 0
}

func runPluginRemove(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plugin remove", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("plugin-dir", "", "managed generic plugin directory")
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 || !validOutputFormat(*format) {
		fmt.Fprintln(stderr, "usage: platform-factory plugin remove [--plugin-dir DIR] [--format text|json] NAME")
		return 2
	}
	name := flags.Arg(0)
	if strings.ContainsAny(name, `/\\`) || name == "." || name == ".." {
		fmt.Fprintln(stderr, "platform-factory plugin remove: invalid plugin name")
		return 2
	}
	root, err := pluginapp.GenericPluginDir(*dir)
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
	if *format == "json" {
		return writePluginMutationJSON(stdout, stderr, "remove", name, "removed", "")
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
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 || *language == "" || !validOutputFormat(*format) {
		fmt.Fprintln(stderr, "usage: platform-factory plugin create --language LANGUAGE [--dialect js|ts] [--output DIR] [--format text|json] NAME")
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
	if *format == "json" {
		cmd.Stdout = io.Discard
	}
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin create: language plugin %s cannot scaffold this plugin: %v\n", *language, err)
		return 1
	}
	if *format == "json" {
		return writePluginMutationJSON(stdout, stderr, "create", flags.Arg(0), "created", *output)
	}
	return 0
}

func runPluginLoad(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plugin load", flag.ContinueOnError)
	flags.SetOutput(stderr)
	from := flags.String("from", "", "path to a plugin binary to load (required unless NAME is a built-in language: "+pluginapp.BuiltinLanguageList()+")")
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 || !validOutputFormat(*format) {
		fmt.Fprintln(stderr, "usage: platform-factory plugin load [--from PATH] [--format text|json] NAME")
		return 2
	}
	name := flags.Arg(0)
	source := *from
	if source == "" {
		found, err := pluginapp.LocateBuiltinPluginBinary(name)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory plugin load: %v\n", err)
			return 1
		}
		source = found
	} else {
		resolved, cleanup, err := pluginapp.PrepareSource(source)
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
	if *format == "json" {
		return writePluginMutationJSON(stdout, stderr, "load", name, "loaded", installedPath)
	}
	fmt.Fprintf(stdout, "loaded %s (%s)\n", name, installedPath)
	return 0
}

func runPluginUnload(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plugin unload", flag.ContinueOnError)
	flags.SetOutput(stderr)
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 || !validOutputFormat(*format) {
		fmt.Fprintln(stderr, "usage: platform-factory plugin unload [--format text|json] NAME")
		return 2
	}
	name := flags.Arg(0)
	if err := langplugin.Unload(name); err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin unload: %v\n", err)
		return 1
	}
	if *format == "json" {
		return writePluginMutationJSON(stdout, stderr, "unload", name, "unloaded", "")
	}
	fmt.Fprintf(stdout, "unloaded %s\n", name)
	return 0
}

func writePluginMutationJSON(stdout, stderr io.Writer, operation, name, status, path string) int {
	result := struct {
		APIVersion string `json:"api_version"`
		Operation  string `json:"operation"`
		Resource   string `json:"resource"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		Path       string `json:"path,omitempty"`
	}{cliOutputAPIVersion, operation, "plugin", name, status, path}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin %s: encode output: %v\n", operation, err)
		return 1
	}
	return 0
}

type pluginListEntry struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
}

func runPluginList(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plugin list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("plugin-dir", "", "managed generic plugin directory")
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || (*format != "text" && *format != "json") {
		fmt.Fprintln(stderr, "usage: platform-factory plugin list [--plugin-dir DIR] [--format text|json]")
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
	entries := make([]pluginListEntry, 0, len(pluginapp.BuiltinLanguages)+len(loadedSet))
	for _, name := range pluginapp.BuiltinLanguages {
		status := "not loaded"
		if loadedSet[name] {
			status = "loaded"
		}
		entries = append(entries, pluginListEntry{Name: name, Kind: "language", Status: status})
		delete(loadedSet, name)
	}
	var custom []string
	for name := range loadedSet {
		custom = append(custom, name)
	}
	sort.Strings(custom)
	for _, name := range custom {
		entries = append(entries, pluginListEntry{Name: name, Kind: "language-custom", Status: "loaded"})
	}
	root, err := pluginapp.GenericPluginDir(*dir)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin list: %v\n", err)
		return 1
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return writePluginList(entries, *format, stdout, stderr)
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
		entries = append(entries, pluginListEntry{Name: plugin.Manifest.Name, Kind: "rpc", Status: "installed", Version: plugin.Manifest.Version})
	}
	return writePluginList(entries, *format, stdout, stderr)
}

func writePluginList(entries []pluginListEntry, format string, stdout, stderr io.Writer) int {
	if format == "json" {
		if err := json.NewEncoder(stdout).Encode(map[string]any{"api_version": cliOutputAPIVersion, "plugins": entries}); err != nil {
			fmt.Fprintf(stderr, "platform-factory plugin list: encode output: %v\n", err)
			return 1
		}
		return 0
	}
	for _, entry := range entries {
		suffix := entry.Status
		if entry.Kind == "language-custom" {
			suffix = "loaded (custom)"
		} else if entry.Kind == "rpc" {
			suffix = fmt.Sprintf("installed (RPC, %s)", entry.Version)
		}
		fmt.Fprintf(stdout, "%-10s %s\n", entry.Name, suffix)
	}
	return 0
}

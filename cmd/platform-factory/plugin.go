// `pf plugin load/unload/list` - the user-facing management surface
// for the language plugins dispatched from language_plugin.go. This is
// the only thing a user needs to know: they never touch $PATH, a
// binary name, or where platform-factory keeps things - `pf plugin
// load python` makes `language_plugin: true` work for a Python
// project, `pf plugin unload python` turns it back off, `pf plugin
// list` shows what's on. Everything here is built on top of
// sdk/langplugin, the same public package a third-party plugin author
// would use - the CLI has no special access the SDK doesn't.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/CYPT71/secure-oci-base/sdk/langplugin"
)

// builtinLanguages are the languages platform-factory ships a
// plugins/lang-<name> module for - see docs/language-plugin-layers.md.
// `pf plugin load <name>` can find one of these on its own; anything
// else needs an explicit --from.
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
	case "unload":
		return runPluginUnload(args[1:], stdout, stderr)
	case "list":
		return runPluginList(args[1:], stdout, stderr)
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
  platform-factory plugin unload NAME
  platform-factory plugin list

NAME is one of `+builtinLanguageList()+` for a language platform-factory
already ships a plugin for, or any name you choose together with
--from PATH for a plugin of your own. --from PATH may point at an
already-built binary, or a directory containing its Go module source
(go.mod) - the latter is built for you before loading.

Examples:
  platform-factory plugin load python
  platform-factory plugin load --from ./my-plugin-binary my-plugin
  platform-factory plugin load --from ./my-plugin-source my-plugin
  platform-factory plugin list
  platform-factory plugin unload python`)
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
		return from, noop, nil
	}
	if !info.IsDir() {
		return "", noop, fmt.Errorf("%s is neither a file nor a directory", from)
	}
	if _, err := os.Stat(filepath.Join(from, "go.mod")); err != nil {
		return "", noop, fmt.Errorf("%s is a directory but has no go.mod - point --from at a plugin binary, or a directory containing its Go module source", from)
	}
	tmp, err := os.CreateTemp("", "platform-factory-plugin-build-*")
	if err != nil {
		return "", noop, fmt.Errorf("create temporary build output: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpPath) // go build creates it itself; a stale empty file would only get in the way
	cleanup = func() { _ = os.Remove(tmpPath) }

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
	return 0
}

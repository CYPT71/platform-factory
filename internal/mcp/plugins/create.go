package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CYPT71/platform-factory/internal/mcp/toolerror"
	hostplugin "github.com/CYPT71/platform-factory/internal/plugin"
)

// zeroDigest is the same all-zero sha256 stand-in value
// plugins/containerd/plugin.json already ships in source control for an
// executable that is built later, never checked in - a freshly
// scaffolded plugin has nothing to hash yet, and this is the
// established in-repo convention for saying so rather than inventing a
// new one.
const zeroDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// CreateRequest is the pf_plugin_create input.
type CreateRequest struct {
	Name         string                       `json:"name"`
	Description  string                       `json:"description"`
	Capabilities []string                     `json:"capabilities"`
	Family       string                       `json:"family"`
	Permissions  hostplugin.PluginPermissions `json:"permissions"`
}

// CreateResult is the pf_plugin_create output: exactly the files it
// wrote, so a caller (human or the server-embedded agent) knows what to
// review before committing.
type CreateResult struct {
	Plugin    string   `json:"plugin"`
	Path      string   `json:"path"`
	Files     []string `json:"files"`
	NextSteps []string `json:"next_steps"`
}

// Create scaffolds a brand-new plugin under plugins/<name>. Two
// fundamentally different shapes exist, and which one gets written
// depends entirely on family:
//
//   - family: "language" scaffolds a language-command plugin: a flat
//     plugins/<name>/main.go dispatching plain inspect/freeze/build-layer/
//     scaffold subcommands over sdk/langplugin.Dispatch (JSON on stdout,
//     no RPC framing, no plugin.json manifest) - the exact shape
//     `platform-factory plugin load`'s probe (langplugin.RunInspection)
//     expects, and the only family `pf_plugin_load`/`pf plugin load` can
//     ever install. See plugins/lang-node/main.go for the real pattern
//     this mirrors.
//   - every other family (analyzer/build/runtime/deployment/capability)
//     scaffolds an RPC plugin: a plugin.json manifest, a README, a go.mod
//     using this repository's own "depends on the main module via a
//     local replace" convention (see plugins/lang-python/go.mod), and a
//     cmd/platform-factory-<name>/main.go that starts a real
//     sdk/plugin.Server and registers a Handle for every requested
//     capability - installed via `pf_plugin_load`/`platform-factory
//     plugin install`, never `load`.
//
// Both refuse to write anything if plugins/<name> already exists, and
// only ever write inside that one new directory (plus registering it in
// the repository's own go.work, so a fresh scaffold is buildable
// immediately rather than failing with "main module ... does not
// contain package" until someone edits go.work by hand).
func Create(ctx context.Context, repoRoot string, req CreateRequest) (CreateResult, error) {
	if err := validPluginName(req.Name); err != nil {
		return CreateResult{}, err
	}
	if strings.TrimSpace(req.Description) == "" {
		return CreateResult{}, toolerror.New(toolerror.ErrInvalidArgument, "description must not be empty")
	}
	if len(req.Capabilities) == 0 {
		return CreateResult{}, toolerror.New(toolerror.ErrInvalidArgument, "at least one capability is required")
	}
	family := req.Family
	if family == "" {
		family = string(hostplugin.PluginFamilyCapability)
	}

	dir := filepath.Join(pluginsDir(repoRoot), req.Name)
	if _, err := os.Stat(dir); err == nil {
		return CreateResult{}, toolerror.New(toolerror.ErrAlreadyExists, "plugins/%s already exists", req.Name)
	} else if !os.IsNotExist(err) {
		return CreateResult{}, fmt.Errorf("check plugins/%s: %w", req.Name, err)
	}

	if family == string(hostplugin.PluginFamilyLanguage) {
		return createLanguagePlugin(repoRoot, dir, req)
	}

	executableName := "platform-factory-" + req.Name
	manifest := hostplugin.Manifest{
		APIVersion:   hostplugin.ManifestAPIVersion,
		Name:         req.Name,
		Version:      "0.1.0",
		Capabilities: append([]string(nil), req.Capabilities...),
		Family:       hostplugin.PluginFamily(family),
		Platforms:    []string{"linux/amd64", "linux/arm64"},
		Permissions:  req.Permissions,
		Executable:   executableName,
		Digest:       zeroDigest,
	}
	if err := manifest.Validate(); err != nil {
		return CreateResult{}, toolerror.New(toolerror.ErrValidationFailed, "requested plugin would not pass manifest validation: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "cmd", executableName), 0o755); err != nil {
		return CreateResult{}, fmt.Errorf("create plugins/%s: %w", req.Name, err)
	}

	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return CreateResult{}, err
	}
	files := map[string]string{
		"plugin.json": string(manifestJSON) + "\n",
		"README.md":   renderReadme(req, executableName),
		"go.mod":      renderGoMod(req.Name),
		filepath.Join("cmd", executableName, "main.go"): renderMain(req, executableName),
	}

	var written []string
	for relative, content := range files {
		full := filepath.Join(dir, relative)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return CreateResult{}, fmt.Errorf("create %s: %w", relative, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return CreateResult{}, fmt.Errorf("write %s: %w", relative, err)
		}
		written = append(written, filepath.ToSlash(filepath.Join("plugins", req.Name, relative)))
	}

	if err := addPluginToGoWork(repoRoot, req.Name); err != nil {
		return CreateResult{}, fmt.Errorf("register plugins/%s in go.work: %w", req.Name, err)
	}

	return CreateResult{
		Plugin: req.Name,
		Path:   "plugins/" + req.Name,
		Files:  written,
		NextSteps: []string{
			fmt.Sprintf("Implement each capability handler in plugins/%s/cmd/%s/main.go - every one currently returns a not-yet-implemented error.", req.Name, executableName),
			fmt.Sprintf("go build -o plugins/%s/%s ./plugins/%s/cmd/%s", req.Name, executableName, req.Name, executableName),
			fmt.Sprintf("Recompute the manifest digest (sha256 of the built binary) and update plugins/%s/plugin.json - pf_plugin_validate reports the expected value.", req.Name),
			fmt.Sprintf("pf plugin install --from plugins/%s", req.Name),
			"pf_plugin_validate to confirm the manifest, build, and digest before proposing a PR.",
		},
	}, nil
}

// createLanguagePlugin scaffolds a language-command plugin: a flat
// plugins/<name>/main.go dispatching inspect/freeze/build-layer/scaffold
// over sdk/langplugin.Dispatch, a go.mod depending on sdk/langplugin
// (not sdk/plugin), and a README describing `pf plugin load --from`
// (not `pf plugin install`, which only ever installs the other, RPC
// family of plugin). No plugin.json is written - language plugins have
// no manifest, no digest pin, no signature; `platform-factory plugin
// load` installs a bare binary into sdk/langplugin's own managed
// directory. req.Capabilities and req.Permissions don't apply to this
// family (the language-plugin contract is the fixed four subcommands
// below, not an open capability set) and are accepted but unused, same
// as any other family-specific field a request doesn't need.
func createLanguagePlugin(repoRoot, dir string, req CreateRequest) (CreateResult, error) {
	files := map[string]string{
		"go.mod":    renderLangGoMod(req.Name),
		"main.go":   renderLangMain(req),
		"README.md": renderLangReadme(req),
	}

	var written []string
	for relative, content := range files {
		full := filepath.Join(dir, relative)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return CreateResult{}, fmt.Errorf("create %s: %w", relative, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return CreateResult{}, fmt.Errorf("write %s: %w", relative, err)
		}
		written = append(written, filepath.ToSlash(filepath.Join("plugins", req.Name, relative)))
	}

	if err := addPluginToGoWork(repoRoot, req.Name); err != nil {
		return CreateResult{}, fmt.Errorf("register plugins/%s in go.work: %w", req.Name, err)
	}

	executableName := "platform-factory-lang-" + req.Name
	return CreateResult{
		Plugin: req.Name,
		Path:   "plugins/" + req.Name,
		Files:  written,
		NextSteps: []string{
			fmt.Sprintf("Fill in runInspect's langplugin.Definition in plugins/%s/main.go with real marker files, source extensions, and entrypoints for this language.", req.Name),
			fmt.Sprintf("Implement runFreeze (and dependenciesRelPath) in plugins/%s/main.go once the language has a real dependency-install step.", req.Name),
			fmt.Sprintf("go build -o %s ./plugins/%s", executableName, req.Name),
			fmt.Sprintf("platform-factory plugin load --from plugins/%s/%s %s", req.Name, executableName, req.Name),
			fmt.Sprintf("platform-factory detect PROJECT_DIR - confirm it now recognizes %s once Definition is filled in.", req.Name),
		},
	}, nil
}

func renderReadme(req CreateRequest, executableName string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n\n", req.Name, req.Description)
	b.WriteString("## Capabilities\n\n")
	for _, capability := range req.Capabilities {
		fmt.Fprintf(&b, "- `%s`\n", capability)
	}
	b.WriteString("\n## Build\n\n```\ngo build -o ")
	b.WriteString(executableName)
	b.WriteString(" ./cmd/")
	b.WriteString(executableName)
	b.WriteString("\n```\n\n")
	b.WriteString("After building, update plugin.json's `digest` field to the sha256 of the built ")
	b.WriteString("binary (the manifest ships with an all-zero digest until then, the same convention ")
	b.WriteString("plugins/containerd/plugin.json uses for its own not-yet-built executable).\n\n")
	b.WriteString("## Install\n\n```\nplatform-factory plugin install --from plugins/")
	b.WriteString(req.Name)
	b.WriteString(" [--key PUBLIC.pem]\n```\n")
	return b.String()
}

func renderGoMod(name string) string {
	return fmt.Sprintf(`// This module depends on the main github.com/CYPT71/platform-factory
// module only for sdk/plugin (the plugin-side RPC server SDK) - the
// same require+replace pattern plugins/lang-python and every other
// in-repo plugin module already uses for their own sdk dependencies.
module github.com/CYPT71/platform-factory/plugins/%s

go 1.25.12

require github.com/CYPT71/platform-factory v0.0.2

replace github.com/CYPT71/platform-factory => ../..
`, name)
}

func renderMain(req CreateRequest, executableName string) string {
	var handlers strings.Builder
	for _, capability := range req.Capabilities {
		fmt.Fprintf(&handlers, "\tserver.Handle(%q, notYetImplemented(%q))\n", capability, capability)
	}
	return fmt.Sprintf(`// Command %s implements the %q plugin's capabilities over the
// sdk/plugin RPC protocol (stdin/stdout, framed JSON-RPC-style
// messages - see sdk/plugin/server.go). Every capability below starts
// out returning a clear "not yet implemented" error: a manifest
// capability with no registered handler would otherwise fail with an
// opaque "unknown method" 404 at dispatch time instead.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/CYPT71/platform-factory/sdk/plugin"
)

var version = "0.1.0"

func main() {
	server := plugin.NewServer(%q, version)
%s
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "%s:", err)
		os.Exit(1)
	}
}

func notYetImplemented(capability string) plugin.Handler {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		return nil, fmt.Errorf("%%s: not yet implemented", capability)
	}
}
`, executableName, req.Name, req.Name, handlers.String(), executableName)
}

func renderLangGoMod(name string) string {
	return fmt.Sprintf(`// This module depends on the main github.com/CYPT71/platform-factory
// module only for sdk/langplugin (the shared deterministic-tar writer
// and inspect/dispatch mechanics) - the same require+replace pattern
// plugins/lang-node and every other language plugin already uses.
module github.com/CYPT71/platform-factory/plugins/%s

go 1.25.12

require github.com/CYPT71/platform-factory v0.0.2

replace github.com/CYPT71/platform-factory => ../..
`, name)
}

func renderLangReadme(req CreateRequest) string {
	executableName := "platform-factory-lang-" + req.Name
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n\n", req.Name, req.Description)
	b.WriteString("This is a language-command plugin: `platform-factory` execs it directly ")
	b.WriteString("as `inspect`/`freeze`/`build-layer` subcommands (see sdk/langplugin.Dispatch), ")
	b.WriteString("never over the RPC-over-stdio protocol pf_plugin_create's other families use.\n\n")
	b.WriteString("## Build\n\n```\ngo build -o ")
	b.WriteString(executableName)
	b.WriteString(" ./plugins/")
	b.WriteString(req.Name)
	b.WriteString("\n```\n\n")
	b.WriteString("## Load\n\nNo manifest, no digest, no signature - `platform-factory plugin load` ")
	b.WriteString("installs the built binary straight into sdk/langplugin's own managed directory ")
	b.WriteString("and probes it immediately:\n\n```\nplatform-factory plugin load --from ")
	b.WriteString(executableName)
	b.WriteString(" ")
	b.WriteString(req.Name)
	b.WriteString("\n```\n")
	return b.String()
}

// renderLangMain generates a flat, buildable, immediately-loadable
// language plugin: langplugin.Inspect's Definition starts empty (never
// matches anything, which is a safe default - pf_plugin_load's probe
// only requires a well-formed response, not a correct one), and
// freeze/scaffold return a clear not-yet-implemented error the same way
// renderMain's RPC scaffold does for every capability, until someone
// fills in the language-specific parts. Mirrors plugins/lang-node/main.go's
// real structure.
func renderLangMain(req CreateRequest) string {
	executableName := "platform-factory-lang-" + req.Name
	return fmt.Sprintf(`// Command %s implements the %q language plugin's
// inspect/freeze/build-layer/scaffold contract over plain CLI
// subcommands (see sdk/langplugin.Dispatch) - the protocol
// %q's own probe (langplugin.RunInspection) actually speaks: a
// subprocess invocation with JSON on stdout, not the sdk/plugin
// RPC-over-stdio protocol pf_plugin_create's other families use. See
// plugins/lang-node/main.go for a fully worked-out example of this
// same shape.
package main

import (
	"fmt"
	"os"

	"github.com/CYPT71/platform-factory/sdk/langplugin"
)

func main() {
	err := langplugin.Dispatch(os.Args[1:], map[string]langplugin.Handler{
		"inspect": runInspect, "freeze": runFreeze,
		"build-layer": runBuildLayer, "scaffold": runScaffold,
	})
	if err == langplugin.ErrUsage {
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %%v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: %s <inspect|freeze|build-layer|scaffold> [OPTIONS]")
	fmt.Fprintln(os.Stderr, "  inspect --root DIR")
	fmt.Fprintln(os.Stderr, "  freeze --root DIR")
	fmt.Fprintln(os.Stderr, "  build-layer --root DIR --output TAR --dest PREFIX")
	fmt.Fprintln(os.Stderr, "  scaffold --name NAME --output DIR")
}

// runInspect answers whether root looks like a %s project. TODO: replace
// the empty Definition below with real marker files, source extensions,
// and entrypoints for %s (see sdk/langplugin.Definition's doc comment
// and plugins/lang-node/main.go's runInspect for a worked example).
func runInspect(args []string) error {
	root, err := langplugin.ParseRootFlag("inspect", args)
	if err != nil {
		return err
	}
	result, err := langplugin.Inspect(root, langplugin.Definition{
		Language: %q,
		Profile:  %q,
		// TODO: real marker files/extensions/entrypoints for this language.
		Markers:          []string{},
		SourceExtensions: []string{},
		Entrypoints:      []string{},
	})
	if err != nil {
		return err
	}
	return langplugin.WriteInspection(result)
}

// dependenciesRelPath is where runFreeze is expected to write %s's
// dependency snapshot, relative to the project root - update this once
// runFreeze is implemented for real (see plugins/lang-node/main.go's
// depsRelPath for the convention).
const dependenciesRelPath = "TODO-dependencies-directory"

// TODO: implement the real dependency-install step for %s.
func runFreeze(args []string) error {
	if _, err := langplugin.ParseRootFlag("freeze", args); err != nil {
		return err
	}
	return fmt.Errorf("freeze: not yet implemented for %s")
}

func runBuildLayer(args []string) error {
	return langplugin.BuildLayer(args, dependenciesRelPath, %q)
}

// TODO: implement scaffold (a new-project generator for %s), or remove
// this subcommand from the map in main() above if this language plugin
// doesn't need one - see plugins/lang-node/main.go's runScaffold for a
// worked example.
func runScaffold(args []string) error {
	return fmt.Errorf("scaffold: not yet implemented for %s")
}
`, executableName, req.Name, executableName,
		executableName, executableName,
		req.Name, req.Name,
		req.Name, req.Name,
		req.Name,
		req.Name,
		req.Name,
		executableName,
		req.Name,
		req.Name)
}

// addPluginToGoWork appends "./plugins/<name>" to the repository's own
// go.work "use (...)" block, if it isn't already there - without this,
// `go build`/`go test` on a freshly scaffolded plugin module fails with
// "main module ... does not contain package" until someone edits
// go.work by hand, since go.work (not any single go.mod) is what makes
// a plugins/<name> module part of this workspace's build at all.
func addPluginToGoWork(repoRoot, name string) error {
	path := filepath.Join(repoRoot, "go.work")
	entry := "./plugins/" + name
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read go.work: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	useOpen, useClose := -1, -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if useOpen == -1 && trimmed == "use (" {
			useOpen = i
			continue
		}
		if useOpen != -1 && trimmed == entry {
			return nil // already registered
		}
		if useOpen != -1 && useClose == -1 && trimmed == ")" {
			useClose = i
			break
		}
	}
	if useOpen == -1 || useClose == -1 {
		return fmt.Errorf("go.work has no recognizable \"use (\" ... \")\" block")
	}
	inserted := append([]string(nil), lines[:useClose]...)
	inserted = append(inserted, "\t"+entry)
	inserted = append(inserted, lines[useClose:]...)
	return os.WriteFile(path, []byte(strings.Join(inserted, "\n")), 0o644)
}

type createArguments struct {
	Name         string                       `json:"name"`
	Description  string                       `json:"description"`
	Capabilities []string                     `json:"capabilities"`
	Family       string                       `json:"family"`
	Permissions  hostplugin.PluginPermissions `json:"permissions"`
}

// CreateToolHandler returns the pf_plugin_create handler.
func CreateToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var args createArguments
		decoder := json.NewDecoder(strings.NewReader(string(arguments)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&args); err != nil {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
		}
		result, err := Create(ctx, repoRoot, CreateRequest{
			Name: args.Name, Description: args.Description,
			Capabilities: args.Capabilities, Family: args.Family, Permissions: args.Permissions,
		})
		if err != nil {
			return "", err
		}
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

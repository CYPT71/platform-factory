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

// Create scaffolds a brand-new RPC plugin under plugins/<name>: a
// plugin.json manifest, a README, a go.mod using this repository's own
// "depends on the main module via a local replace" convention (see
// plugins/lang-python/go.mod), and a cmd/platform-factory-<name>/main.go
// that starts a real sdk/plugin.Server and registers a Handle for every
// requested capability. It refuses to write anything if plugins/<name>
// already exists, and only ever writes inside that one new directory.
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

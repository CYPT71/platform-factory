// Package plugins implements the pf_plugin_* tools and the
// pf://plugins, pf://plugins/schema, pf://marketplace resources: a
// read-only and creation interface onto this repository's own plugins/
// directory. It has no dependency on the internal/mcp package itself,
// matching internal/mcp/project's shape - handlers are plain functions
// over stdlib types, wired into an *mcp.Server by internal/mcp's own
// registration code.
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CYPT71/platform-factory/internal/mcp/toolerror"
	hostplugin "github.com/CYPT71/platform-factory/internal/plugin"
)

// Summary is one plugins/<name> directory's identity, enough to decide
// whether it's worth a full pf_plugin_inspect call.
type Summary struct {
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	Kind         string   `json:"kind"` // "rpc" (has plugin.json) or "language-command" (sdk/langplugin single-script/binary convention)
	Family       string   `json:"family,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Version      string   `json:"version,omitempty"`
}

// Detail is the pf_plugin_inspect payload: a Summary plus everything a
// caller needs before deciding to modify a plugin.
type Detail struct {
	Summary
	Manifest  *hostplugin.Manifest `json:"manifest,omitempty"`
	Module    string               `json:"module"` // "standalone", "depends-on-main", or "none"
	TestFiles []string             `json:"test_files,omitempty"`
}

func pluginsDir(repoRoot string) string { return filepath.Join(repoRoot, "plugins") }

// ListPlugins enumerates every plugins/<name> directory. A directory
// with a plugin.json is reported as "rpc" using LoadManifest's own
// validation (a directory whose manifest fails to parse is reported
// with an empty Capabilities/Family rather than failing the whole
// listing, since one broken plugin should not hide every other one from
// an inspecting client). A directory without plugin.json is reported as
// "language-command" - this repository's other real plugin shape (see
// plugins/lang-python et al., loaded via `pf plugin load`, not
// `pf plugin install`).
func ListPlugins(repoRoot string) ([]Summary, error) {
	entries, err := os.ReadDir(pluginsDir(repoRoot))
	if err != nil {
		return nil, fmt.Errorf("list plugins: %w", err)
	}
	var summaries []Summary
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		summaries = append(summaries, summarize(repoRoot, entry.Name()))
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Name < summaries[j].Name })
	return summaries, nil
}

func summarize(repoRoot, name string) Summary {
	dir := filepath.Join(pluginsDir(repoRoot), name)
	summary := Summary{Name: name, Path: "plugins/" + name, Kind: "language-command"}
	if _, err := os.Stat(filepath.Join(dir, hostplugin.ManifestFileName)); err == nil {
		summary.Kind = "rpc"
		if manifest, err := hostplugin.LoadManifest(dir); err == nil {
			summary.Family = string(manifest.Family)
			summary.Capabilities = manifest.Capabilities
			summary.Version = manifest.Version
		}
	}
	return summary
}

// moduleKind reports whether dir has its own go.mod, and if so whether
// that go.mod's own leading comment/require records the "depends on the
// main module via a local replace" convention every current in-repo
// plugin module uses (see plugins/lang-python/go.mod).
func moduleKind(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "none"
	}
	if strings.Contains(string(data), "replace github.com/CYPT71/platform-factory =>") {
		return "depends-on-main"
	}
	return "standalone"
}

func testFiles(dir string) []string {
	var files []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			relative, relErr := filepath.Rel(dir, path)
			if relErr == nil {
				files = append(files, filepath.ToSlash(relative))
			}
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// InspectPlugin returns the full detail for one named plugin.
func InspectPlugin(repoRoot, name string) (Detail, error) {
	if err := validPluginName(name); err != nil {
		return Detail{}, err
	}
	dir := filepath.Join(pluginsDir(repoRoot), name)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return Detail{}, toolerror.New(toolerror.ErrNotFound, "plugin %q not found under plugins/", name)
	}
	detail := Detail{
		Summary:   summarize(repoRoot, name),
		Module:    moduleKind(dir),
		TestFiles: testFiles(dir),
	}
	if detail.Kind == "rpc" {
		manifest, err := hostplugin.LoadManifest(dir)
		if err != nil {
			return Detail{}, toolerror.New(toolerror.ErrValidationFailed, "plugin %q manifest: %v", name, err)
		}
		detail.Manifest = &manifest
	}
	return detail, nil
}

// validPluginName is deliberately the same constraint plugin.Manifest's
// own Validate() enforces on Name - reused here (rather than
// re-implemented against a different regex) purely as an early,
// path-traversal-relevant guard: it also guarantees name can never
// contain "/" or "..", so filepath.Join(pluginsDir(repoRoot), name)
// cannot escape the plugins/ directory.
func validPluginName(name string) error {
	if name == "" {
		return toolerror.New(toolerror.ErrInvalidArgument, "plugin name must not be empty")
	}
	for _, r := range name {
		if !(r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return toolerror.New(toolerror.ErrInvalidArgument, "invalid plugin name %q: must match ^[a-z][a-z0-9-]{0,62}$", name)
		}
	}
	if name[0] < 'a' || name[0] > 'z' {
		return toolerror.New(toolerror.ErrInvalidArgument, "invalid plugin name %q: must start with a lowercase letter", name)
	}
	if len(name) > 63 {
		return toolerror.New(toolerror.ErrInvalidArgument, "invalid plugin name %q: must be at most 63 characters", name)
	}
	return nil
}

type inspectArguments struct {
	Plugin string `json:"plugin"`
}

// ListToolHandler returns the pf_plugin_list handler.
func ListToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		summaries, err := ListPlugins(repoRoot)
		if err != nil {
			return "", err
		}
		encoded, err := json.MarshalIndent(summaries, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

// InspectToolHandler returns the pf_plugin_inspect handler.
func InspectToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var args inspectArguments
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
		}
		detail, err := InspectPlugin(repoRoot, args.Plugin)
		if err != nil {
			return "", err
		}
		encoded, err := json.MarshalIndent(detail, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

// PluginsResourceHandler returns the pf://plugins resource handler.
func PluginsResourceHandler(repoRoot string) func(context.Context) (string, string, error) {
	return func(ctx context.Context) (string, string, error) {
		summaries, err := ListPlugins(repoRoot)
		if err != nil {
			return "", "", err
		}
		encoded, err := json.MarshalIndent(summaries, "", "  ")
		if err != nil {
			return "", "", err
		}
		return string(encoded), "application/json", nil
	}
}

package plugins

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	pluginapp "github.com/CYPT71/platform-factory/internal/app/plugin"
	"github.com/CYPT71/platform-factory/internal/mcp/toolerror"
)

// LanguagePluginBackend is sdk/langplugin's registry operations
// (Load/RunInspection/Unload), injected because internal packages must
// not depend on sdk/ adapters directly (see
// cmd/platform-factory/language_plugin.go for the established pattern
// this mirrors). Production code always supplies the real
// sdk/langplugin functions from cmd/platform-factory/mcp.go, the one
// place allowed to import both internal/mcp and sdk/langplugin.
type LanguagePluginBackend struct {
	Load    func(name, sourcePath string) (installedPath string, err error)
	Inspect func(binary, root string) error
	Unload  func(name string) error
}

// LoadRequest is the pf_plugin_load input.
type LoadRequest struct {
	Name string `json:"name"`
	From string `json:"from"`
}

// LoadResult is the pf_plugin_load output.
type LoadResult struct {
	Plugin        string `json:"plugin"`
	InstalledPath string `json:"installed_path"`
}

// Load installs a language plugin into the host's language-plugin
// directory (sdk/langplugin's registry - $PLATFORM_FACTORY_LANG_PLUGIN_DIR
// or ~/.platform-factory/plugins - independent of repoRoot, same as
// `platform-factory plugin load` at cmd/platform-factory/plugin.go's
// runPluginLoad) and probes it with a real inspection run before
// keeping it, unloading again on a failed probe so a bad plugin never
// stays registered silently.
func Load(ctx context.Context, backend LanguagePluginBackend, req LoadRequest) (LoadResult, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return LoadResult{}, toolerror.New(toolerror.ErrInvalidArgument, "name must not be empty")
	}

	source := strings.TrimSpace(req.From)
	if source == "" {
		found, err := pluginapp.LocateBuiltinPluginBinary(name)
		if err != nil {
			// LocateBuiltinPluginBinary's own message already names the
			// exact fix (build the binary, or pass --from) - safe to
			// surface verbatim, and a well-defined, expected condition
			// rather than an internal failure.
			return LoadResult{}, toolerror.New(toolerror.ErrNotFound, "%v", err)
		}
		source = found
	} else {
		resolved, cleanup, err := pluginapp.PrepareSource(source)
		defer cleanup()
		if err != nil {
			return LoadResult{}, toolerror.New(toolerror.ErrInvalidArgument, "%v", err)
		}
		source = resolved
	}

	installedPath, err := backend.Load(name, source)
	if err != nil {
		return LoadResult{}, err
	}

	probeRoot, probeErr := os.MkdirTemp("", "platform-factory-plugin-probe-*")
	if probeErr == nil {
		probeErr = backend.Inspect(installedPath, probeRoot)
		_ = os.RemoveAll(probeRoot)
	}
	if probeErr != nil {
		_ = backend.Unload(name)
		return LoadResult{}, toolerror.New(toolerror.ErrValidationFailed, "plugin failed the inspect contract and was not loaded: %v", probeErr)
	}

	return LoadResult{Plugin: name, InstalledPath: installedPath}, nil
}

type loadArguments struct {
	Name string `json:"name"`
	From string `json:"from"`
}

// LoadToolHandler returns the pf_plugin_load handler. Unlike the other
// pf_plugin_* tools it does not operate on repoRoot: it targets the
// host-global language-plugin directory, so it takes no repository path
// argument - only the injected backend.
func LoadToolHandler(backend LanguagePluginBackend) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var args loadArguments
		decoder := json.NewDecoder(strings.NewReader(string(arguments)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&args); err != nil {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
		}
		result, err := Load(ctx, backend, LoadRequest{Name: args.Name, From: args.From})
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

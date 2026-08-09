// Host-side dispatch for the separate-module language plugin pattern
// (plugins/lang-<language>, e.g. plugins/lang-python) - see
// docs/language-plugin-layers.md. This is deliberately unrelated to the
// sdk/plugin subprocess-RPC protocol (plugins.go, examples/sdk/plugin-*):
// a language plugin here is a plain binary, `exec`'d the same simple way
// plugins/containerd and plugins/kubevirt already are
// (platform-factory-kubevirt, platform-factory-containerd), not a
// long-lived process speaking framed JSON-RPC over stdin/stdout.
//
// The binary is never looked up on bare $PATH: it's resolved through
// sdk/langplugin.Resolve, which only ever finds a binary a user
// explicitly installed via `pf plugin load <language>` (see plugin.go).
// That's the whole registry - present in
// sdk/langplugin.Dir() means loaded, absent means not, and Resolve's
// error message already tells the user the one command that fixes it.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/CYPT71/secure-oci-base/internal/project"
	"github.com/CYPT71/secure-oci-base/sdk/langplugin"
)

// pluginResolver locates the absolute path to a loaded language
// plugin's binary - production code always passes
// sdk/langplugin.Resolve; tests inject a fake to avoid touching the
// real plugin directory.
type pluginResolver func(language string) (string, error)

// languagePluginDestPrefix is the container path every language
// plugin's contributed layer is rooted at, namespaced by language so
// two different projects' plugin layers (should either ever be combined
// - they are not today) could never collide.
func languagePluginDestPrefix(language string) string {
	return "app/.platform-factory/deps/" + language
}

// languagePluginLayer runs `<resolved binary> build-layer` when
// loaded.Config.LanguagePlugin is set and returns the path to the tar
// file it produced - the caller adds this to oci.Options.ExtraLayers,
// which independently validates and re-hashes every byte of it (see
// internal/oci/extralayers.go); nothing here is trusted just because the
// plugin exited 0. Returns ("", nil, nil) when the project hasn't opted
// in at all - the common case, and the only case for every project that
// predates this feature.
//
// The returned cleanup func removes the temporary tar file and must
// always be called (even on a non-nil error, where it may still have
// been created) once the caller is done with it.
func languagePluginLayer(loaded project.Loaded, stderr io.Writer, execute projectExecutor, resolve pluginResolver) (tarPath string, cleanup func(), err error) {
	noop := func() {}
	if !loaded.Config.LanguagePlugin {
		return "", noop, nil
	}
	if loaded.Config.Language == "" {
		return "", noop, errors.New("language_plugin is set but language is empty")
	}
	binary, err := resolve(loaded.Config.Language)
	if err != nil {
		return "", noop, err
	}

	tmp, err := os.CreateTemp("", "platform-factory-lang-layer-*.tar")
	if err != nil {
		return "", noop, fmt.Errorf("create temporary layer file: %w", err)
	}
	tarPath = tmp.Name()
	cleanup = func() { _ = os.Remove(tarPath) }
	if err := tmp.Close(); err != nil {
		return "", cleanup, err
	}
	// The plugin needs to create tarPath itself (os.Create, not append to
	// an existing file), so remove the empty placeholder CreateTemp left
	// behind - the plugin's own os.Create call will fail loudly if it
	// can't recreate it, which is a clearer signal than a silently
	// truncated/appended-to file would be.
	if err := os.Remove(tarPath); err != nil {
		return "", cleanup, fmt.Errorf("prepare temporary layer path: %w", err)
	}

	args := []string{
		"build-layer",
		"--root", loaded.Root,
		"--output", tarPath,
		"--dest", languagePluginDestPrefix(loaded.Config.Language),
	}
	fmt.Fprintf(stderr, "platform-factory: language plugin: %s\n", formatCommand(binary, args))
	if err := execute(binary, args, loaded.Root, stderr, stderr); err != nil {
		return "", cleanup, fmt.Errorf("%s build-layer: %w", binary, err)
	}
	if info, statErr := os.Stat(tarPath); statErr != nil || !info.Mode().IsRegular() {
		return "", cleanup, fmt.Errorf("%s build-layer exited successfully but did not create %s", binary, tarPath)
	}
	return tarPath, cleanup, nil
}

// resolveLoadedPlugin is the production pluginResolver, backed by the
// same registry `pf plugin load`/`pf plugin unload` manage.
func resolveLoadedPlugin(language string) (string, error) {
	return langplugin.Resolve(language)
}

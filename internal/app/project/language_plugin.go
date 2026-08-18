package project

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/CYPT71/platform-factory/internal/project"
	"github.com/CYPT71/platform-factory/internal/shellquote"
)

// LanguagePluginResolver locates the absolute path to a loaded language
// plugin's binary - production code always passes
// sdk/langplugin.Resolve; this package cannot import sdk/ itself (see
// the package doc comment), so the caller always supplies it. Tests
// inject a fake to avoid touching the real plugin directory.
type LanguagePluginResolver func(language string) (string, error)

// LanguagePluginDestPrefix is the container path every language
// plugin's contributed layer is rooted at, namespaced by language so
// two different projects' plugin layers (should either ever be
// combined - they are not today) could never collide.
func LanguagePluginDestPrefix(language string) string {
	return "app/.platform-factory/deps/" + language
}

// LanguagePluginLayer runs `<resolved binary> build-layer` when
// loaded.Config.LanguagePlugin is set and returns the path to the tar
// file it produced - the caller adds this to oci.Options.ExtraLayers,
// which independently validates and re-hashes every byte of it (see
// oci/extralayers.go); nothing here is trusted just because the
// plugin exited 0. Returns ("", nil, nil) when the project hasn't opted
// in at all - the common case, and the only case for every project that
// predates this feature.
//
// The returned cleanup func removes the temporary tar file and must
// always be called (even on a non-nil error, where it may still have
// been created) once the caller is done with it.
// execute runs a resolved plugin binary with args in directory,
// streaming its output to stdout/stderr - an unnamed func type
// (structurally, not nominally, matched) so callers can pass
// cmd/platform-factory's own named projectExecutor value directly,
// without a conversion.
func LanguagePluginLayer(loaded project.Loaded, stderr io.Writer, execute func(name string, args []string, directory string, stdout, stderr io.Writer) error, resolve LanguagePluginResolver) (tarPath string, cleanup func(), err error) {
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
		"--dest", LanguagePluginDestPrefix(loaded.Config.Language),
	}
	fmt.Fprintf(stderr, "platform-factory: language plugin: %s\n", shellquote.Command(binary, args))
	if err := execute(binary, args, loaded.Root, stderr, stderr); err != nil {
		return "", cleanup, fmt.Errorf("%s build-layer: %w", binary, err)
	}
	if info, statErr := os.Stat(tarPath); statErr != nil || !info.Mode().IsRegular() {
		return "", cleanup, fmt.Errorf("%s build-layer exited successfully but did not create %s", binary, tarPath)
	}
	return tarPath, cleanup, nil
}

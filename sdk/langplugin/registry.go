package langplugin

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// dirEnv overrides the managed plugin directory Dir returns - used by
// tests to stay hermetic, and available to anyone else who needs
// per-invocation control over where loaded plugins live.
const dirEnv = "PLATFORM_FACTORY_LANG_PLUGIN_DIR"

// Dir returns the directory `pf plugin load`/`pf plugin unload` manage
// language plugin binaries in. platform-factory resolves every language
// plugin it execs from here - never from bare $PATH - so "loaded" has
// exactly one meaning: the binary is present in this directory. Set
// PLATFORM_FACTORY_LANG_PLUGIN_DIR to override it.
func Dir() (string, error) {
	if dir := os.Getenv(dirEnv); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".platform-factory", "plugins"), nil
}

func binaryName(name string) string {
	if runtime.GOOS == "windows" {
		return "platform-factory-lang-" + name + ".exe"
	}
	return "platform-factory-lang-" + name
}

// Resolve returns the absolute path to name's loaded plugin binary. It
// fails with an actionable message - not a bare "file not found" - when
// name was never loaded, since that's the ordinary, expected case for
// any language a project hasn't opted into yet.
func Resolve(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, binaryName(name))
	info, err := os.Stat(path)
	if err == nil && info.Mode().IsRegular() {
		return path, nil
	}
	if err == nil {
		return "", fmt.Errorf("language plugin %q is loaded at %s, but that isn't a regular file", name, path)
	}
	if adjacent, adjacentErr := adjacentBinary(name); adjacentErr == nil {
		return adjacent, nil
	}
	return "", fmt.Errorf("language plugin %q isn't installed - reinstall platform-factory or run `pf plugin load %s`", name, name)
}

// adjacentBinary finds plugins shipped as part of the same installation as
// the running CLI. This keeps a fresh install self-contained while the managed
// directory remains available for user-installed overrides.
func adjacentBinary(name string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	path := filepath.Join(filepath.Dir(self), binaryName(name))
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", os.ErrNotExist
	}
	return path, nil
}

// Load installs the binary at sourcePath as name's plugin, so a later
// Resolve(name) finds it immediately - no core restart, no PATH to
// edit. The copy is atomic (written to a temp file in the same
// directory, then renamed into place) so a Resolve racing a Load never
// observes a partially-written binary.
func Load(name, sourcePath string) (installedPath string, err error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", sourcePath, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", sourcePath)
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create plugin directory %s: %w", dir, err)
	}
	destination := filepath.Join(dir, binaryName(name))
	temporary := destination + ".loading"
	if err := copyExecutable(sourcePath, temporary); err != nil {
		_ = os.Remove(temporary)
		return "", err
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return "", fmt.Errorf("install %s: %w", destination, err)
	}
	return destination, nil
}

func copyExecutable(sourcePath, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", sourcePath, err)
	}
	defer source.Close()
	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create %s: %w", destinationPath, err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		return fmt.Errorf("write %s: %w", destinationPath, err)
	}
	return destination.Close()
}

// Unload removes name's loaded plugin binary, if any. Unloading a
// language that was never loaded is not an error - the end state
// (not loaded) is what the caller asked for either way.
func Unload(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	dir, err := Dir()
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dir, binaryName(name)))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("unload %q: %w", name, err)
	}
	return nil
}

func validateName(name string) error {
	if name == "" {
		return errors.New("plugin name must not be empty")
	}
	for index, character := range name {
		valid := character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || (index > 0 && (character == '-' || character == '_' || character == '.'))
		if !valid {
			return fmt.Errorf("invalid plugin name %q (use lowercase letters, digits, '-', '_' or '.')", name)
		}
	}
	return nil
}

// List returns the name of every language currently loaded (its plugin
// binary is present in Dir()), sorted alphabetically. An empty,
// nonexistent, or unreadable directory is reported as no plugins
// loaded, not an error - that's the ordinary state for a project that
// has never run `pf plugin load`.
func List() ([]string, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read plugin directory %s: %w", dir, err)
		}
		entries = nil
	}
	const prefix = "platform-factory-lang-"
	namesSet := make(map[string]struct{})
	collect := func(entries []os.DirEntry) {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			fileName := entry.Name()
			if strings.HasSuffix(fileName, ".loading") {
				continue // an install still in progress, or one that was interrupted
			}
			fileName = strings.TrimSuffix(fileName, ".exe")
			if !strings.HasPrefix(fileName, prefix) {
				continue
			}
			namesSet[strings.TrimPrefix(fileName, prefix)] = struct{}{}
		}
	}
	collect(entries)
	if self, executableErr := os.Executable(); executableErr == nil {
		if adjacentEntries, readErr := os.ReadDir(filepath.Dir(self)); readErr == nil {
			collect(adjacentEntries)
		}
	}
	var names []string
	for name := range namesSet {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

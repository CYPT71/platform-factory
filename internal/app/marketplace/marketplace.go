// Package marketplace is the application-layer service behind `pf
// marketplace`'s self-contained business rules: resolving the index/
// sources/plugins paths, splitting a NAME[@VERSION] argument, loading
// trusted signing keys, and checking release verification status. Most
// of `pf marketplace`'s real logic already lives in internal/marketplace
// (index, sync, search, install/update/remove) - cmd/platform-factory/
// marketplace.go calls that directly, plus this package for the small
// pieces that were still stuck in the CLI file. The `tui` subcommand is
// a deliberate exception: cmd/tui/marketplacetui already talks to
// internal/marketplace directly during its own event loop, so it
// bypasses this package rather than adding an indirection with no
// benefit today.
package marketplace

import (
	"crypto/ed25519"
	"os"
	"strings"

	"github.com/CYPT71/platform-factory/internal/marketplace"
	hostplugin "github.com/CYPT71/platform-factory/internal/plugin"
)

// Paths resolves the local index, sources, and installed-plugins
// directory: PLATFORM_FACTORY_MARKETPLACE_DIR (or the user's config
// directory) for the plugins directory, marketplace's own defaults for
// the index/sources files.
func Paths() (indexPath, sourcesPath, pluginsDir string, err error) {
	indexPath, err = marketplace.DefaultIndexPath()
	if err != nil {
		return "", "", "", err
	}
	sourcesPath, err = marketplace.DefaultSourcesPath()
	if err != nil {
		return "", "", "", err
	}
	dir := os.Getenv("PLATFORM_FACTORY_MARKETPLACE_DIR")
	if dir == "" {
		config, cfgErr := os.UserConfigDir()
		if cfgErr != nil {
			return "", "", "", cfgErr
		}
		dir = config + string(os.PathSeparator) + "platform-factory" + string(os.PathSeparator) + "marketplace"
	}
	pluginsDir = dir + string(os.PathSeparator) + "plugins"
	return indexPath, sourcesPath, pluginsDir, nil
}

// AnyReleaseVerified reports whether plugin has at least one release
// whose checksum and signature were verified at sync time.
func AnyReleaseVerified(plugin marketplace.PluginEntry) bool {
	for _, release := range plugin.Releases {
		if release.Verified {
			return true
		}
	}
	return false
}

// SplitNameVersion splits a NAME[@VERSION] argument, e.g. "acme@v1.2.0"
// into ("acme", "v1.2.0") or "acme" into ("acme", "").
func SplitNameVersion(arg string) (name, version string) {
	if idx := strings.LastIndex(arg, "@"); idx > 0 {
		return arg[:idx], arg[idx+1:]
	}
	return arg, ""
}

// LoadKeys loads every trusted Ed25519 publisher public key file.
func LoadKeys(files []string) ([]ed25519.PublicKey, error) {
	keys := make([]ed25519.PublicKey, 0, len(files))
	for _, filename := range files {
		key, err := hostplugin.LoadPublicKey(filename)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, nil
}

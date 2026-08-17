package marketplacetui

import (
	"fmt"

	"github.com/CYPT71/platform-factory/internal/marketplace"
)

type pluginItem struct {
	entry            marketplace.PluginEntry
	installedVersion string
}

func (i pluginItem) Title() string {
	title := i.entry.Name + "  " + i.entry.LatestVersion
	if release, ok := i.entry.Latest(); ok && release.Verified {
		title += "  ✓verified"
	}
	if i.installedVersion == i.entry.LatestVersion && i.installedVersion != "" {
		return title + "  [installed]"
	}
	if i.installedVersion != "" {
		return title + fmt.Sprintf("  [installed %s, update available]", i.installedVersion)
	}
	return title
}

func (i pluginItem) Description() string {
	if i.entry.Description != "" {
		return i.entry.Description
	}
	return i.entry.Repository
}

func (i pluginItem) FilterValue() string { return i.entry.Name }

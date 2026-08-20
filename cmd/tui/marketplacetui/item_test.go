package marketplacetui

import (
	"testing"

	"github.com/CYPT71/platform-factory/internal/marketplace"
)

func TestPluginItemTitleDescriptionAndFilterValue(t *testing.T) {
	item := pluginItem{
		entry: marketplace.PluginEntry{
			Name: "python", LatestVersion: "v1.0.0", Description: "Python support",
			Releases: []marketplace.ReleaseEntry{{Version: "v1.0.0", Tag: "v1.0.0", Verified: true}},
		},
	}
	if got := item.Title(); got != "python  v1.0.0  ✓verified" {
		t.Fatalf("Title()=%q", got)
	}
	if got := item.Description(); got != "Python support" {
		t.Fatalf("Description()=%q", got)
	}
	if got := item.FilterValue(); got != "python" {
		t.Fatalf("FilterValue()=%q", got)
	}

	item.entry.Description = ""
	item.entry.Repository = "https://example.com/acme/python.git"
	if got := item.Description(); got != "https://example.com/acme/python.git" {
		t.Fatalf("Description() fallback=%q", got)
	}

	item.installedVersion = "v1.0.0"
	if got := item.Title(); got != "python  v1.0.0  ✓verified  [installed]" {
		t.Fatalf("Title() installed=%q", got)
	}
	item.installedVersion = "v0.9.0"
	if got := item.Title(); got != "python  v1.0.0  ✓verified  [installed v0.9.0, update available]" {
		t.Fatalf("Title() outdated=%q", got)
	}
}

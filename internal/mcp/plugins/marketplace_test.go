package plugins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/marketplace"
)

func TestGatherMarketplaceSummaryReportsNoteWhenEmpty(t *testing.T) {
	t.Setenv("PLATFORM_FACTORY_MARKETPLACE_DIR", t.TempDir())
	summary, err := gatherMarketplaceSummary()
	if err != nil {
		t.Fatalf("gatherMarketplaceSummary: %v", err)
	}
	if len(summary.Repositories) != 0 || len(summary.Plugins) != 0 || summary.Note == "" {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestGatherMarketplaceSummaryReadsSourcesAndIndex(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PLATFORM_FACTORY_MARKETPLACE_DIR", dir)

	sourcesPath, err := marketplace.DefaultSourcesPath()
	if err != nil {
		t.Fatal(err)
	}
	sources := &marketplace.Sources{}
	sources.Add("https://example.com/acme/plugin.git")
	if err := sources.Save(sourcesPath); err != nil {
		t.Fatal(err)
	}

	indexPath, err := marketplace.DefaultIndexPath()
	if err != nil {
		t.Fatal(err)
	}
	index := &marketplace.Index{Plugins: []marketplace.PluginEntry{{Name: "acme-plugin", Repository: "https://example.com/acme/plugin.git"}}}
	if err := index.Save(indexPath); err != nil {
		t.Fatal(err)
	}

	summary, err := gatherMarketplaceSummary()
	if err != nil {
		t.Fatalf("gatherMarketplaceSummary: %v", err)
	}
	if summary.Note != "" {
		t.Errorf("expected no note once sources/index are populated, got %q", summary.Note)
	}
	if len(summary.Repositories) != 1 || summary.Repositories[0] != "https://example.com/acme/plugin.git" {
		t.Errorf("repositories=%v", summary.Repositories)
	}
	if len(summary.Plugins) != 1 || summary.Plugins[0] != "acme-plugin" {
		t.Errorf("plugins=%v", summary.Plugins)
	}
}

func TestMarketplaceResourceHandlerReturnsJSON(t *testing.T) {
	t.Setenv("PLATFORM_FACTORY_MARKETPLACE_DIR", t.TempDir())
	handler := MarketplaceResourceHandler()
	body, mimeType, err := handler(context.Background())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if mimeType != "application/json" {
		t.Fatalf("mimeType=%q", mimeType)
	}
	if !strings.Contains(body, `"indexed_plugins"`) {
		t.Fatalf("body=%s", body)
	}
	var decoded MarketplaceSummary
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

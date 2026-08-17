package plugins

import (
	"context"
	"encoding/json"

	"github.com/CYPT71/platform-factory/internal/marketplace"
)

// MarketplaceSummary is the pf://marketplace payload: this machine's
// locally tracked sources and synced index, read straight off disk (no
// network call - LoadSources/LoadIndex are pure file reads, and both
// tolerate a missing file by returning an empty result rather than an
// error, which is the normal state before the first `pf marketplace
// sync`).
type MarketplaceSummary struct {
	SourcesPath  string   `json:"sources_path,omitempty"`
	Repositories []string `json:"repositories"`
	IndexPath    string   `json:"index_path,omitempty"`
	Plugins      []string `json:"indexed_plugins"`
	Note         string   `json:"note,omitempty"`
}

func gatherMarketplaceSummary() (MarketplaceSummary, error) {
	summary := MarketplaceSummary{Repositories: []string{}, Plugins: []string{}}

	sourcesPath, err := marketplace.DefaultSourcesPath()
	if err == nil {
		summary.SourcesPath = sourcesPath
		if sources, loadErr := marketplace.LoadSources(sourcesPath); loadErr == nil {
			summary.Repositories = append(summary.Repositories, sources.Repositories...)
		}
	}

	indexPath, err := marketplace.DefaultIndexPath()
	if err == nil {
		summary.IndexPath = indexPath
		if index, loadErr := marketplace.LoadIndex(indexPath); loadErr == nil {
			for _, entry := range index.Plugins {
				summary.Plugins = append(summary.Plugins, entry.Name)
			}
		}
	}

	if len(summary.Repositories) == 0 && len(summary.Plugins) == 0 {
		summary.Note = "no marketplace sources or synced index found yet; run `platform-factory marketplace sources add REPO` then `platform-factory marketplace sync`"
	}
	return summary, nil
}

// MarketplaceResourceHandler returns the pf://marketplace resource
// handler.
func MarketplaceResourceHandler() func(context.Context) (string, string, error) {
	return func(ctx context.Context) (string, string, error) {
		summary, err := gatherMarketplaceSummary()
		if err != nil {
			return "", "", err
		}
		encoded, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			return "", "", err
		}
		return string(encoded), "application/json", nil
	}
}

package marketplace

import (
	"testing"
	"time"
)

func sampleIndex() *Index {
	idx := &Index{}
	idx.Upsert(PluginEntry{
		Name: "python", Description: "Python language support", Author: "Platform Factory",
		Tags: []string{"language", "official"}, Downloads: 500,
		Releases:      []ReleaseEntry{{Version: "v1.0.0", Tag: "v1.0.0", Verified: true, PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}},
		LatestVersion: "v1.0.0",
	})
	idx.Upsert(PluginEntry{
		Name: "acme-runtime", Description: "A custom runtime by Acme", Author: "Acme Corp",
		Tags: []string{"runtime"}, Downloads: 50,
		Releases:      []ReleaseEntry{{Version: "v0.3.0", Tag: "v0.3.0", Verified: false, PublishedAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)}},
		LatestVersion: "v0.3.0",
	})
	idx.Upsert(PluginEntry{
		Name: "zzz-analyzer", Description: "Static analysis for everything", Author: "Someone",
		Tags: []string{"analyzer"}, Downloads: 10,
		Releases:      []ReleaseEntry{{Version: "v2.0.0", Tag: "v2.0.0", Verified: false, PublishedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}},
		LatestVersion: "v2.0.0",
	})
	return idx
}

func TestSearchEmptyQueryDefaultsToNameOrder(t *testing.T) {
	result := Search(sampleIndex(), Request{})
	if len(result.Hits) != 3 {
		t.Fatalf("want 3 hits, got %d", len(result.Hits))
	}
	names := []string{result.Hits[0].Plugin.Name, result.Hits[1].Plugin.Name, result.Hits[2].Plugin.Name}
	want := []string{"acme-runtime", "python", "zzz-analyzer"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names=%v, want %v", names, want)
		}
	}
}

func TestSearchFuzzyMatchesNameSubsequence(t *testing.T) {
	result := Search(sampleIndex(), Request{Query: "pytn"})
	if len(result.Hits) != 1 || result.Hits[0].Plugin.Name != "python" {
		t.Fatalf("want a single hit on python, got %+v", result.Hits)
	}
}

func TestSearchMatchesDescriptionAndAuthor(t *testing.T) {
	result := Search(sampleIndex(), Request{Query: "acme"})
	if len(result.Hits) != 1 || result.Hits[0].Plugin.Name != "acme-runtime" {
		t.Fatalf("want a single hit on acme-runtime (author match), got %+v", result.Hits)
	}
}

func TestSearchNoMatchReturnsEmpty(t *testing.T) {
	result := Search(sampleIndex(), Request{Query: "xyznonexistent"})
	if len(result.Hits) != 0 || result.Total != 0 {
		t.Fatalf("want no hits, got %+v", result)
	}
}

func TestSearchFilterVerifiedOnly(t *testing.T) {
	result := Search(sampleIndex(), Request{Filter: Filter{VerifiedOnly: true}})
	if len(result.Hits) != 1 || result.Hits[0].Plugin.Name != "python" {
		t.Fatalf("want only the verified plugin, got %+v", result.Hits)
	}
}

func TestSearchFilterTag(t *testing.T) {
	result := Search(sampleIndex(), Request{Filter: Filter{Tag: "Runtime"}})
	if len(result.Hits) != 1 || result.Hits[0].Plugin.Name != "acme-runtime" {
		t.Fatalf("want only the runtime-tagged plugin (case-insensitive), got %+v", result.Hits)
	}
}

func TestSearchSortPopularity(t *testing.T) {
	result := Search(sampleIndex(), Request{Sort: SortPopularity})
	if result.Hits[0].Plugin.Name != "python" || result.Hits[len(result.Hits)-1].Plugin.Name != "zzz-analyzer" {
		t.Fatalf("unexpected popularity order: %+v", result.Hits)
	}
}

func TestSearchSortDate(t *testing.T) {
	result := Search(sampleIndex(), Request{Sort: SortDate})
	if result.Hits[0].Plugin.Name != "acme-runtime" { // published 2026-06-01, most recent
		t.Fatalf("unexpected date order: %+v", result.Hits)
	}
}

func TestSearchSortVerified(t *testing.T) {
	index := sampleIndex()
	index.Plugins[1].Releases[0].Verified = true
	result := Search(index, Request{Sort: SortVerified})
	if got := result.Hits[0].Plugin.Name; got != index.Plugins[1].Name {
		t.Fatalf("first verified result = %q, want %q", got, index.Plugins[1].Name)
	}
}

func TestSearchPagination(t *testing.T) {
	result := Search(sampleIndex(), Request{Page: 1, PageSize: 2})
	if len(result.Hits) != 2 || result.Total != 3 || result.TotalPages != 2 {
		t.Fatalf("page 1: %+v", result)
	}
	result = Search(sampleIndex(), Request{Page: 2, PageSize: 2})
	if len(result.Hits) != 1 || result.Total != 3 {
		t.Fatalf("page 2: %+v", result)
	}
	result = Search(sampleIndex(), Request{Page: 99, PageSize: 2})
	if len(result.Hits) != 0 {
		t.Fatalf("page past the end should be empty, got %+v", result.Hits)
	}
}

func TestFuzzyScorePrefersExactAndPrefixMatches(t *testing.T) {
	exact, ok := fuzzyScore("python", "python")
	if !ok {
		t.Fatal("exact match should match")
	}
	prefix, ok := fuzzyScore("python", "py")
	if !ok {
		t.Fatal("prefix match should match")
	}
	subsequence, ok := fuzzyScore("python", "ptn")
	if !ok {
		t.Fatal("subsequence match should match")
	}
	if !(exact > prefix && prefix > subsequence) {
		t.Fatalf("want exact > prefix > subsequence, got exact=%d prefix=%d subsequence=%d", exact, prefix, subsequence)
	}
	if _, ok := fuzzyScore("python", "z"); ok {
		t.Fatal("a character absent from the target must not match")
	}
	if _, ok := fuzzyScore("python", "nohtyp"); ok {
		t.Fatal("out-of-order characters must not match")
	}
}

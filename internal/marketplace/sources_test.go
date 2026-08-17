package marketplace

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSourcesAddRemoveIsIdempotent(t *testing.T) {
	s := &Sources{}
	if !s.Add("https://example.com/a.git") {
		t.Fatal("first Add should report newly added")
	}
	if s.Add("https://example.com/a.git") {
		t.Fatal("second Add of the same repository should report not newly added")
	}
	if len(s.Repositories) != 1 {
		t.Fatalf("want 1 repository, got %v", s.Repositories)
	}
	if !s.Remove("https://example.com/a.git") {
		t.Fatal("Remove should report it was present")
	}
	if s.Remove("https://example.com/a.git") {
		t.Fatal("second Remove should report it was already gone")
	}
}

func TestSourcesSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sources.json")
	s := &Sources{}
	s.Add("https://example.com/a.git")
	s.Add("https://example.com/b.git")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadSources(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Repositories) != 2 {
		t.Fatalf("reloaded = %v", reloaded.Repositories)
	}
}

func TestSyncAllUpsertsEveryTrackedRepositoryAndCollectsFailures(t *testing.T) {
	good := newTestRepo(t)
	tagRelease(t, good, "v1.0.0", manifestFor("acme", "v1.0.0"), "plugin.py", "a")

	sources := &Sources{}
	sources.Add(good)
	sources.Add(filepath.Join(t.TempDir(), "does-not-exist"))

	index := &Index{}
	results, failures := SyncAll(context.Background(), index, sources)
	if len(results) != 1 {
		t.Fatalf("want 1 successful result, got %d", len(results))
	}
	if len(failures) != 1 {
		t.Fatalf("want 1 failure, got %v", failures)
	}
	if _, ok := index.Plugin("acme"); !ok {
		t.Fatal("the successfully synced plugin should be in the index")
	}
}

// TestSyncAllWithOptionsTreatsCatalogAndExplicitSourcesIdentically proves
// the coexistence and no-special-trust invariants together: whether a
// repository string in the merged Sources came from
// marketplace-sources.json or was discovered via the (untrusted) public
// catalog, SyncAllWithOptions/SyncSourceWithKeys cannot tell the
// difference and applies the exact same verification either way - an
// unsigned release from either source ends up unverified.
func TestSyncAllWithOptionsTreatsCatalogAndExplicitSourcesIdentically(t *testing.T) {
	explicit := newTestRepo(t)
	tagRelease(t, explicit, "v1.0.0", manifestFor("explicit-plugin", "v1.0.0"), "plugin.py", "a")

	catalogDiscovered := newTestRepo(t)
	tagRelease(t, catalogDiscovered, "v1.0.0", manifestFor("catalog-plugin", "v1.0.0"), "plugin.py", "b")

	// This is exactly what the CLI's `sync` builds in memory: the
	// hand-curated Sources plus whatever the catalog discovered, unioned
	// - never persisted back into marketplace-sources.json.
	merged := &Sources{}
	merged.Add(explicit)
	merged.Add(catalogDiscovered)

	index := &Index{}
	results, failures := SyncAllWithOptions(context.Background(), index, merged, nil, time.Second)
	if len(failures) != 0 {
		t.Fatalf("unexpected failures: %v", failures)
	}
	if len(results) != 2 {
		t.Fatalf("want both repositories synced, got %d", len(results))
	}
	for _, name := range []string{"explicit-plugin", "catalog-plugin"} {
		entry, ok := index.Plugin(name)
		if !ok {
			t.Fatalf("%s should be indexed", name)
		}
		release, ok := entry.Latest()
		if !ok || release.Verified {
			t.Fatalf("%s: unsigned release must not be verified regardless of discovery origin, got %+v", name, release)
		}
	}
}

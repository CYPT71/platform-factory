package marketplace

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadIndexMissingFileReturnsEmpty(t *testing.T) {
	idx, err := LoadIndex(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Plugins) != 0 {
		t.Fatalf("expected an empty index, got %+v", idx.Plugins)
	}
}

func TestIndexSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "index.json")
	idx := &Index{}
	idx.Upsert(PluginEntry{
		Name: "acme-runtime", Repository: "https://example.com/acme/runtime.git",
		Releases: []ReleaseEntry{
			{Version: "v1.0.0", Tag: "v1.0.0", Checksum: "sha256:" + fortyEightHex(), PublishedAt: time.Now().UTC().Truncate(time.Second)},
		},
		LatestVersion: "v1.0.0",
	})
	if err := idx.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	plugin, ok := reloaded.Plugin("acme-runtime")
	if !ok || plugin.Repository != "https://example.com/acme/runtime.git" || plugin.LatestVersion != "v1.0.0" {
		t.Fatalf("unexpected reloaded plugin: %+v ok=%v", plugin, ok)
	}
}

func TestIndexUpsertReplacesExistingEntry(t *testing.T) {
	idx := &Index{}
	idx.Upsert(PluginEntry{Name: "p", Repository: "r1"})
	idx.Upsert(PluginEntry{Name: "p", Repository: "r2"})
	if len(idx.Plugins) != 1 {
		t.Fatalf("want exactly one entry, got %d", len(idx.Plugins))
	}
	plugin, _ := idx.Plugin("p")
	if plugin.Repository != "r2" {
		t.Fatalf("upsert did not replace: %+v", plugin)
	}
}

func TestLoadIndexRejectsUnknownFieldsAndBadVersion(t *testing.T) {
	dir := t.TempDir()
	unknown := filepath.Join(dir, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"version":1,"plugins":[],"extra":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIndex(unknown); err == nil {
		t.Fatal("expected an error for an unknown field")
	}

	badVersion := filepath.Join(dir, "bad-version.json")
	if err := os.WriteFile(badVersion, []byte(`{"version":99,"plugins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIndex(badVersion); err == nil {
		t.Fatal("expected an error for an unsupported index version")
	}
}

func TestLoadIndexRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	if err := os.WriteFile(real, []byte(`{"version":1,"plugins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	idx := &Index{}
	if err := idx.Save(link); err == nil {
		t.Fatal("expected Save to refuse a symlink destination")
	}
}

func fortyEightHex() string {
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}

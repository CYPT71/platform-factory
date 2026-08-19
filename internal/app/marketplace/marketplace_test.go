package marketplace

import (
	"testing"

	"github.com/CYPT71/platform-factory/internal/marketplace"
)

func TestSplitNameVersion(t *testing.T) {
	cases := []struct {
		in, name, version string
	}{
		{"acme", "acme", ""},
		{"acme@v1.2.0", "acme", "v1.2.0"},
		{"@leading-at", "@leading-at", ""},
	}
	for _, c := range cases {
		name, version := SplitNameVersion(c.in)
		if name != c.name || version != c.version {
			t.Errorf("SplitNameVersion(%q) = (%q, %q), want (%q, %q)", c.in, name, version, c.name, c.version)
		}
	}
}

func TestAnyReleaseVerified(t *testing.T) {
	unverified := marketplace.PluginEntry{Releases: []marketplace.ReleaseEntry{{Version: "v1", Verified: false}}}
	if AnyReleaseVerified(unverified) {
		t.Fatal("expected no verified release")
	}
	verified := marketplace.PluginEntry{Releases: []marketplace.ReleaseEntry{
		{Version: "v1", Verified: false}, {Version: "v2", Verified: true},
	}}
	if !AnyReleaseVerified(verified) {
		t.Fatal("expected at least one verified release")
	}
}

func TestPaths(t *testing.T) {
	t.Setenv("PLATFORM_FACTORY_MARKETPLACE_DIR", t.TempDir())
	indexPath, sourcesPath, pluginsDir, err := Paths()
	if err != nil {
		t.Fatal(err)
	}
	if indexPath == "" || sourcesPath == "" || pluginsDir == "" {
		t.Fatalf("paths=%q %q %q", indexPath, sourcesPath, pluginsDir)
	}
}

func TestLoadKeysRejectsMissingFile(t *testing.T) {
	if _, err := LoadKeys([]string{"/does/not/exist.pem"}); err == nil {
		t.Fatal("expected an error for a missing key file")
	}
}

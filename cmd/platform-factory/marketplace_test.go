package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	marketplaceapp "github.com/CYPT71/platform-factory/internal/app/marketplace"
	"github.com/CYPT71/platform-factory/internal/marketplace"
)

// withMarketplaceDir sandboxes every marketplaceapp.Paths()-derived path (index,
// sources, and installed-plugins directory all live under
// PLATFORM_FACTORY_MARKETPLACE_DIR) inside a fresh temp directory, exactly
// like marketplacePaths itself resolves them.
func withMarketplaceDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PLATFORM_FACTORY_MARKETPLACE_DIR", dir)
	return dir
}

func TestPrintMarketplaceUsage(t *testing.T) {
	var buf bytes.Buffer
	printMarketplaceUsage(&buf)
	out := buf.String()
	for _, want := range []string{"platform-factory marketplace", "sources add", "sync", "install", "publish"} {
		if !strings.Contains(out, want) {
			t.Fatalf("usage text missing %q: %s", want, out)
		}
	}
}

func TestRunMarketplaceDispatch(t *testing.T) {
	withMarketplaceDir(t)
	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{"no args", nil, 2, "", "platform-factory marketplace"},
		{"help flag", []string{"-h"}, 0, "platform-factory marketplace", ""},
		{"help word", []string{"help"}, 0, "platform-factory marketplace", ""},
		{"unknown", []string{"bogus"}, 2, "", "unknown subcommand"},
		{"sources no args", []string{"sources"}, 2, "", "usage: platform-factory marketplace sources"},
		{"search empty index", []string{"search"}, 0, "no matching plugins", ""},
		{"list empty", []string{"list"}, 0, "no plugins installed", ""},
		{"remove wrong args", []string{"remove"}, 2, "", "usage: platform-factory marketplace remove"},
		{"install wrong args", []string{"install", "a", "b"}, 2, "", "usage: platform-factory marketplace install"},
		{"update not in index", []string{"update", "nope"}, 1, "", "not installed"},
		// sync with an explicit empty --catalog-url and no tracked sources
		// never reaches the network: it hits the "no sources tracked" early
		// return before FetchCatalog would ever be called.
		{"sync no sources", []string{"sync", "--catalog-url", ""}, 0, "no sources tracked", ""},
		// publish with no catalog URL configured (env unset, no flag) fails
		// entirely locally, before any repository detection.
		{"publish no catalog", []string{"publish"}, 2, "", "no catalog URL configured"},
		// tui with a stray positional argument fails flag parsing/usage
		// before ever trying to start a terminal UI.
		{"tui extra arg", []string{"tui", "extra"}, 2, "", "usage: platform-factory marketplace tui"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runMarketplace(tc.args, &stdout, &stderr)
			if code != tc.wantCode {
				t.Fatalf("code = %d, want %d (stdout=%q stderr=%q)", code, tc.wantCode, stdout.String(), stderr.String())
			}
			if tc.wantOut != "" && !strings.Contains(stdout.String(), tc.wantOut) {
				t.Fatalf("stdout = %q, want substring %q", stdout.String(), tc.wantOut)
			}
			if tc.wantErr != "" && !strings.Contains(stderr.String(), tc.wantErr) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), tc.wantErr)
			}
		})
	}
}

func TestMarketplacePaths(t *testing.T) {
	dir := withMarketplaceDir(t)
	indexPath, sourcesPath, pluginsDir, err := marketplaceapp.Paths()
	if err != nil {
		t.Fatalf("marketplacePaths: %v", err)
	}
	if indexPath != filepath.Join(dir, "marketplace-index.json") {
		t.Fatalf("indexPath = %q", indexPath)
	}
	if sourcesPath != filepath.Join(dir, "marketplace-sources.json") {
		t.Fatalf("sourcesPath = %q", sourcesPath)
	}
	if pluginsDir != dir+string(os.PathSeparator)+"plugins" {
		t.Fatalf("pluginsDir = %q", pluginsDir)
	}
}

func TestRunMarketplaceSources(t *testing.T) {
	withMarketplaceDir(t)
	var stdout, stderr bytes.Buffer

	if code := runMarketplaceSources(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("no args: code = %d", code)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceSources([]string{"add", "https://example.com/plugin.git"}, &stdout, &stderr); code != 0 {
		t.Fatalf("add: code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "tracking https://example.com/plugin.git") {
		t.Fatalf("add stdout = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceSources([]string{"add", "https://example.com/plugin.git"}, &stdout, &stderr); code != 0 {
		t.Fatalf("add duplicate: code = %d", code)
	}
	if !strings.Contains(stdout.String(), "already tracked") {
		t.Fatalf("add duplicate stdout = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceSources([]string{"list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("list: code = %d", code)
	}
	if strings.TrimSpace(stdout.String()) != "https://example.com/plugin.git" {
		t.Fatalf("list stdout = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceSources([]string{"remove", "https://example.com/plugin.git"}, &stdout, &stderr); code != 0 {
		t.Fatalf("remove: code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "untracked") {
		t.Fatalf("remove stdout = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceSources([]string{"remove", "https://example.com/plugin.git"}, &stdout, &stderr); code != 1 {
		t.Fatalf("remove missing: code = %d", code)
	}
	if !strings.Contains(stderr.String(), "is not tracked") {
		t.Fatalf("remove missing stderr = %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceSources([]string{"add"}, &stdout, &stderr); code != 2 {
		t.Fatalf("add wrong arity: code = %d", code)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceSources([]string{"remove"}, &stdout, &stderr); code != 2 {
		t.Fatalf("remove wrong arity: code = %d", code)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceSources([]string{"bogus"}, &stdout, &stderr); code != 2 {
		t.Fatalf("unknown subcommand: code = %d", code)
	}
}

func TestMarketplaceSourcesMachineOutput(t *testing.T) {
	withMarketplaceDir(t)
	repository := "https://example.com/plugin.git"
	var stdout, stderr bytes.Buffer
	if code := runMarketplaceSources([]string{"add", "--format", "json", repository}, &stdout, &stderr); code != 0 {
		t.Fatalf("add code=%d stderr=%s", code, stderr.String())
	}
	requireCLIOutputV1(t, stdout.Bytes(), "operation", "resource", "name", "status")
	stdout.Reset()
	if code := runMarketplaceSources([]string{"list", "--format", "json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("list code=%d stderr=%s", code, stderr.String())
	}
	document := requireCLIOutputV1(t, stdout.Bytes(), "sources")
	if !strings.Contains(string(document["sources"]), repository) {
		t.Fatalf("sources output=%s", stdout.String())
	}
	stdout.Reset()
	if code := runMarketplaceSources([]string{"remove", "--format", "json", repository}, &stdout, &stderr); code != 0 {
		t.Fatalf("remove code=%d stderr=%s", code, stderr.String())
	}
	requireCLIOutputV1(t, stdout.Bytes(), "operation", "resource", "name", "status")
}

func TestMarketplaceSyncEmptyMachineOutput(t *testing.T) {
	withMarketplaceDir(t)
	var stdout, stderr bytes.Buffer
	if code := runMarketplaceSync([]string{"--catalog-url", "", "--format", "json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	requireCLIOutputV1(t, stdout.Bytes(), "operation", "resource", "synced", "configured_sources", "catalog_discovered", "catalog_rejected", "new_releases", "failures")
}

func TestSplitMarketplaceSearchArgs(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantOptions []string
		wantQuery   []string
		wantErr     bool
	}{
		{"plain query", []string{"scaleway", "runtime"}, nil, []string{"scaleway", "runtime"}, false},
		{"flag with value", []string{"--tag", "kvm", "runtime"}, []string{"--tag", "kvm"}, []string{"runtime"}, false},
		{"flag with equals", []string{"--tag=kvm", "runtime"}, []string{"--tag=kvm"}, []string{"runtime"}, false},
		{"verified boolean", []string{"--verified", "runtime"}, []string{"--verified"}, []string{"runtime"}, false},
		{"help flag", []string{"-h"}, []string{"-h"}, nil, false},
		{"dangling flag", []string{"--tag"}, nil, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options, query, err := splitMarketplaceSearchArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !stringSlicesEqual(options, tc.wantOptions) {
				t.Fatalf("options = %v, want %v", options, tc.wantOptions)
			}
			if !stringSlicesEqual(query, tc.wantQuery) {
				t.Fatalf("query = %v, want %v", query, tc.wantQuery)
			}
		})
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRunMarketplaceSearch(t *testing.T) {
	withMarketplaceDir(t)
	indexPath, _, _, err := marketplaceapp.Paths()
	if err != nil {
		t.Fatalf("marketplacePaths: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := runMarketplaceSearch(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("empty index: code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no matching plugins") {
		t.Fatalf("empty index stdout = %q", stdout.String())
	}

	index := &marketplace.Index{Plugins: []marketplace.PluginEntry{{
		Name:          "acme-runtime",
		Description:   "Acme runtime plugin",
		Repository:    "https://example.com/acme-runtime.git",
		LatestVersion: "v1.0.0",
		Downloads:     42,
		Releases: []marketplace.ReleaseEntry{{
			Version: "v1.0.0", Tag: "v1.0.0", Checksum: "sha256:deadbeef", Verified: true,
		}},
	}}}
	if err := index.Save(indexPath); err != nil {
		t.Fatalf("index.Save: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceSearch(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("populated index: code = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"acme-runtime", "v1.0.0", "yes", "42", "page 1/1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("search output missing %q: %s", want, out)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceSearch([]string{"--tag"}, &stdout, &stderr); code != 2 {
		t.Fatalf("dangling flag: code = %d", code)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceSearch([]string{"--page=notanumber"}, &stdout, &stderr); code != 2 {
		t.Fatalf("bad flag value: code = %d", code)
	}
}

func TestAnyReleaseVerified(t *testing.T) {
	if marketplaceapp.AnyReleaseVerified(marketplace.PluginEntry{}) {
		t.Fatal("empty plugin should report unverified")
	}
	unverified := marketplace.PluginEntry{Releases: []marketplace.ReleaseEntry{{Version: "v1", Verified: false}}}
	if marketplaceapp.AnyReleaseVerified(unverified) {
		t.Fatal("no verified releases should report unverified")
	}
	verified := marketplace.PluginEntry{Releases: []marketplace.ReleaseEntry{
		{Version: "v1", Verified: false},
		{Version: "v2", Verified: true},
	}}
	if !marketplaceapp.AnyReleaseVerified(verified) {
		t.Fatal("plugin with a verified release should report verified")
	}
}

func TestSplitNameVersion(t *testing.T) {
	cases := []struct {
		arg         string
		wantName    string
		wantVersion string
	}{
		{"acme-runtime@v1.2.0", "acme-runtime", "v1.2.0"},
		{"acme-runtime", "acme-runtime", ""},
		{"@v1.2.0", "@v1.2.0", ""}, // idx==0 is not >0, entire string is treated as the name
	}
	for _, tc := range cases {
		name, version := marketplaceapp.SplitNameVersion(tc.arg)
		if name != tc.wantName || version != tc.wantVersion {
			t.Fatalf("marketplaceapp.SplitNameVersion(%q) = (%q, %q), want (%q, %q)", tc.arg, name, version, tc.wantName, tc.wantVersion)
		}
	}
}

func writePEMKeyFile(t *testing.T, path string, blockType string, der []byte) {
	t.Helper()
	data := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
}

func TestLoadMarketplaceKeys(t *testing.T) {
	dir := t.TempDir()

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal ed25519 key: %v", err)
	}
	validPath := filepath.Join(dir, "valid.pem")
	writePEMKeyFile(t, validPath, "PUBLIC KEY", der)

	keys, err := marketplaceapp.LoadKeys([]string{validPath})
	if err != nil {
		t.Fatalf("loadMarketplaceKeys valid: %v", err)
	}
	if len(keys) != 1 || !keys[0].Equal(pub) {
		t.Fatalf("loadMarketplaceKeys returned unexpected keys: %v", keys)
	}

	if keys, err := marketplaceapp.LoadKeys(nil); err != nil || len(keys) != 0 {
		t.Fatalf("loadMarketplaceKeys empty: keys=%v err=%v", keys, err)
	}

	missingPath := filepath.Join(dir, "missing.pem")
	if _, err := marketplaceapp.LoadKeys([]string{missingPath}); err == nil {
		t.Fatal("expected error for missing key file")
	}

	badPEMPath := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(badPEMPath, []byte("not a pem file"), 0o600); err != nil {
		t.Fatalf("write bad pem: %v", err)
	}
	if _, err := marketplaceapp.LoadKeys([]string{badPEMPath}); err == nil {
		t.Fatal("expected error for non-PEM key file")
	}

	wrongTypePath := filepath.Join(dir, "wrongtype.pem")
	writePEMKeyFile(t, wrongTypePath, "CERTIFICATE", der)
	if _, err := marketplaceapp.LoadKeys([]string{wrongTypePath}); err == nil {
		t.Fatal("expected error for wrong PEM block type")
	}

	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa key: %v", err)
	}
	ecDER, err := x509.MarshalPKIXPublicKey(&ecKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal ecdsa key: %v", err)
	}
	nonEd25519Path := filepath.Join(dir, "ecdsa.pem")
	writePEMKeyFile(t, nonEd25519Path, "PUBLIC KEY", ecDER)
	if _, err := marketplaceapp.LoadKeys([]string{nonEd25519Path}); err == nil {
		t.Fatal("expected error for non-Ed25519 key")
	}
}

func TestRunMarketplaceInstallErrorPaths(t *testing.T) {
	withMarketplaceDir(t)
	var stdout, stderr bytes.Buffer

	if code := runMarketplaceInstall(nil, &stdout, &stderr, false); code != 2 {
		t.Fatalf("install no args: code = %d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceInstall([]string{"a", "b"}, &stdout, &stderr, false); code != 2 {
		t.Fatalf("install too many args: code = %d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceInstall([]string{"--bogus-flag"}, &stdout, &stderr, false); code != 2 {
		t.Fatalf("install bad flag: code = %d", code)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceInstall([]string{"nonexistent-plugin"}, &stdout, &stderr, false); code != 1 {
		t.Fatalf("install not in index: code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not in the index") {
		t.Fatalf("install not in index stderr = %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceInstall([]string{"nonexistent-plugin"}, &stdout, &stderr, true); code != 1 {
		t.Fatalf("update not installed: code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not installed") {
		t.Fatalf("update not installed stderr = %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	badKeyPath := filepath.Join(t.TempDir(), "missing.pem")
	if code := runMarketplaceInstall([]string{"--key", badKeyPath, "name"}, &stdout, &stderr, false); code != 1 {
		t.Fatalf("install bad key: code = %d, stderr=%s", code, stderr.String())
	}
}

func TestRunMarketplaceRemove(t *testing.T) {
	withMarketplaceDir(t)
	var stdout, stderr bytes.Buffer

	if code := runMarketplaceRemove(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("no args: code = %d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceRemove([]string{"a", "b"}, &stdout, &stderr); code != 2 {
		t.Fatalf("too many args: code = %d", code)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceRemove([]string{"never-installed"}, &stdout, &stderr); code != 0 {
		t.Fatalf("remove idempotent: code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "removed never-installed") {
		t.Fatalf("remove stdout = %q", stdout.String())
	}
}

func TestMarketplaceRemoveMachineOutput(t *testing.T) {
	withMarketplaceDir(t)
	var stdout, stderr bytes.Buffer
	if code := runMarketplaceRemove([]string{"--format", "json", "never-installed"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	document := requireCLIOutputV1(t, stdout.Bytes(), "operation", "resource", "name", "status")
	if string(document["operation"]) != `"remove"` || string(document["resource"]) != `"marketplace_plugin"` || string(document["status"]) != `"removed"` {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestRunMarketplaceList(t *testing.T) {
	dir := withMarketplaceDir(t)
	var stdout, stderr bytes.Buffer

	if code := runMarketplaceList(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("empty list: code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no plugins installed") {
		t.Fatalf("empty list stdout = %q", stdout.String())
	}

	indexPath, _, pluginsDir, err := marketplaceapp.Paths()
	if err != nil {
		t.Fatalf("marketplacePaths: %v", err)
	}
	index := &marketplace.Index{Plugins: []marketplace.PluginEntry{{
		Name: "acme-runtime", Repository: "https://example.com/acme-runtime.git", LatestVersion: "v1.1.0",
		Releases: []marketplace.ReleaseEntry{{Version: "v1.1.0", Tag: "v1.1.0", Checksum: "sha256:x"}},
	}, {
		Name: "up-to-date-plugin", Repository: "https://example.com/utd.git", LatestVersion: "v2.0.0",
		Releases: []marketplace.ReleaseEntry{{Version: "v2.0.0", Tag: "v2.0.0", Checksum: "sha256:y"}},
	}}}
	if err := index.Save(indexPath); err != nil {
		t.Fatalf("index.Save: %v", err)
	}

	manager := &marketplace.Manager{Dir: pluginsDir}
	if err := manager.Remove("seed"); err != nil {
		t.Fatalf("seed manager dir: %v", err)
	}
	installedJSON := `{
  "version": 1,
  "plugins": [
    {"name": "acme-runtime", "version": "v1.0.0", "repository": "https://example.com/acme-runtime.git", "tag": "v1.0.0", "checksum": "sha256:x", "entrypoint": "main", "installed_at": "2026-01-01T00:00:00Z"},
    {"name": "up-to-date-plugin", "version": "v2.0.0", "repository": "https://example.com/utd.git", "tag": "v2.0.0", "checksum": "sha256:y", "entrypoint": "main", "installed_at": "2026-01-01T00:00:00Z"}
  ]
}`
	if err := os.WriteFile(filepath.Join(dir, "plugins", "installed.json"), []byte(installedJSON), 0o600); err != nil {
		t.Fatalf("write installed.json: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceList(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("populated list: code = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"acme-runtime", "v1.0.0", "v1.1.0", "up-to-date-plugin", "v2.0.0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list output missing %q: %s", want, out)
		}
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	var acmeLine, utdLine string
	for _, line := range lines {
		if strings.HasPrefix(line, "acme-runtime") {
			acmeLine = line
		}
		if strings.HasPrefix(line, "up-to-date-plugin") {
			utdLine = line
		}
	}
	if !strings.HasSuffix(strings.TrimRight(acmeLine, " "), "yes") {
		t.Fatalf("expected acme-runtime to show an available update, got line: %q", acmeLine)
	}
	if strings.HasSuffix(strings.TrimRight(utdLine, " "), "yes") {
		t.Fatalf("expected up-to-date-plugin to show no available update, got line: %q", utdLine)
	}
}

func TestHostVersionForCompatibility(t *testing.T) {
	if hostVersionForCompatibility() != version {
		t.Fatalf("hostVersionForCompatibility() = %q, want %q", hostVersionForCompatibility(), version)
	}
}

func TestRunMarketplaceTUIErrorPaths(t *testing.T) {
	withMarketplaceDir(t)
	var stdout, stderr bytes.Buffer

	if code := runMarketplaceTUI([]string{"extra", "args"}, &stdout, &stderr); code != 2 {
		t.Fatalf("extra args: code = %d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runMarketplaceTUI([]string{"--bogus-flag"}, &stdout, &stderr); code != 2 {
		t.Fatalf("bad flag: code = %d", code)
	}
	stdout.Reset()
	stderr.Reset()
	missing := filepath.Join(t.TempDir(), "missing.pem")
	if code := runMarketplaceTUI([]string{"--key", missing}, &stdout, &stderr); code != 1 {
		t.Fatalf("bad key: code = %d, stderr=%s", code, stderr.String())
	}
}

func TestRunMarketplacePublishErrorPaths(t *testing.T) {
	var stdout, stderr bytes.Buffer
	t.Setenv("PLATFORM_FACTORY_MARKETPLACE_CATALOG_URL", "")

	if code := runMarketplacePublish(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("no catalog url: code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no catalog URL configured") {
		t.Fatalf("stderr = %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMarketplacePublish([]string{"--catalog-url", "https://example.com/catalog.json", "extra"}, &stdout, &stderr); code != 2 {
		t.Fatalf("extra args: code = %d", code)
	}

	stdout.Reset()
	stderr.Reset()
	notARepo := t.TempDir()
	if code := runMarketplacePublish([]string{"--catalog-url", "https://example.com/catalog.json", "--dir", notARepo}, &stdout, &stderr); code != 1 {
		t.Fatalf("not a git repo: code = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestRunMarketplaceSyncNoSources(t *testing.T) {
	withMarketplaceDir(t)
	var stdout, stderr bytes.Buffer
	if code := runMarketplaceSync([]string{"--catalog-url", ""}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no sources tracked") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

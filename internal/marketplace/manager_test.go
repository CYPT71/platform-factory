package marketplace

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
)

// syncedIndex builds a real Index from repo via SyncSource - manager
// tests always install against checksums SyncSource itself computed, the
// same way a real sync-then-install flow would, rather than a synthetic
// index that might not match what a real clone produces.
func syncedIndex(t *testing.T, repo string) *Index {
	t.Helper()
	result, err := SyncSource(context.Background(), repo, PluginEntry{})
	if err != nil {
		t.Fatal(err)
	}
	idx := &Index{}
	idx.Upsert(result.Plugin)
	return idx
}

func TestManagerInstallPlacesVerifiedContent(t *testing.T) {
	repo := newTestRepo(t)
	tagRelease(t, repo, "v1.0.0", manifestFor("acme", "v1.0.0"), "plugin.py", "print('hello')")
	idx := syncedIndex(t, repo)

	manager := &Manager{Dir: t.TempDir(), AllowUnsigned: true}
	installed, err := manager.Install(context.Background(), idx, "acme", "")
	if err != nil {
		t.Fatal(err)
	}
	if installed.Version != "v1.0.0" || installed.Tag != "v1.0.0" {
		t.Fatalf("unexpected installed record: %+v", installed)
	}
	content, err := os.ReadFile(filepath.Join(manager.Dir, "acme", "plugin.py"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "print('hello')" {
		t.Fatalf("entrypoint content = %q", content)
	}
	list, err := manager.Installed()
	if err != nil || len(list) != 1 || list[0].Name != "acme" {
		t.Fatalf("Installed() = %+v, err=%v", list, err)
	}
}

func TestManagerInstallRefusesIfAlreadyInstalled(t *testing.T) {
	repo := newTestRepo(t)
	tagRelease(t, repo, "v1.0.0", manifestFor("acme", "v1.0.0"), "plugin.py", "a")
	idx := syncedIndex(t, repo)
	manager := &Manager{Dir: t.TempDir(), AllowUnsigned: true}
	if _, err := manager.Install(context.Background(), idx, "acme", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), idx, "acme", ""); err == nil {
		t.Fatal("expected a second Install to be refused")
	}
}

func TestManagerUpdateRefusesIfNotInstalled(t *testing.T) {
	repo := newTestRepo(t)
	tagRelease(t, repo, "v1.0.0", manifestFor("acme", "v1.0.0"), "plugin.py", "a")
	idx := syncedIndex(t, repo)
	manager := &Manager{Dir: t.TempDir(), AllowUnsigned: true}
	if _, err := manager.Update(context.Background(), idx, "acme", ""); err == nil {
		t.Fatal("expected Update to be refused for a plugin that was never installed")
	}
}

func TestManagerUpdateReplacesInstalledVersion(t *testing.T) {
	repo := newTestRepo(t)
	tagRelease(t, repo, "v1.0.0", manifestFor("acme", "v1.0.0"), "plugin.py", "old")
	tagRelease(t, repo, "v1.1.0", manifestFor("acme", "v1.1.0"), "plugin.py", "new")
	idx := syncedIndex(t, repo)

	manager := &Manager{Dir: t.TempDir(), AllowUnsigned: true}
	if _, err := manager.Install(context.Background(), idx, "acme", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	updated, err := manager.Update(context.Background(), idx, "acme", "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != "v1.1.0" {
		t.Fatalf("updated version = %q, want v1.1.0", updated.Version)
	}
	content, err := os.ReadFile(filepath.Join(manager.Dir, "acme", "plugin.py"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new" {
		t.Fatalf("entrypoint content after update = %q, want %q", content, "new")
	}
	if _, err := os.Stat(filepath.Join(manager.Dir, "acme.previous")); !os.IsNotExist(err) {
		t.Fatalf("backup directory should be cleaned up after a successful update, stat err=%v", err)
	}
}

func TestManagerRemoveIsIdempotent(t *testing.T) {
	repo := newTestRepo(t)
	tagRelease(t, repo, "v1.0.0", manifestFor("acme", "v1.0.0"), "plugin.py", "a")
	idx := syncedIndex(t, repo)
	manager := &Manager{Dir: t.TempDir(), AllowUnsigned: true}
	if _, err := manager.Install(context.Background(), idx, "acme", ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove("acme"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove("acme"); err != nil {
		t.Fatalf("removing an already-removed plugin must be idempotent, got %v", err)
	}
	list, err := manager.Installed()
	if err != nil || len(list) != 0 {
		t.Fatalf("Installed() after remove = %+v, err=%v", list, err)
	}
	if _, err := os.Stat(filepath.Join(manager.Dir, "acme")); !os.IsNotExist(err) {
		t.Fatalf("plugin directory should be gone, stat err=%v", err)
	}
}

func TestManagerInstallRejectsChecksumMismatch(t *testing.T) {
	repo := newTestRepo(t)
	tagRelease(t, repo, "v1.0.0", manifestFor("acme", "v1.0.0"), "plugin.py", "a")
	idx := syncedIndex(t, repo)

	// Tamper the indexed checksum, simulating a tag that moved after it
	// was indexed (or a compromised index).
	plugin, _ := idx.Plugin("acme")
	plugin.Releases[0].Checksum = "sha256:" + fortyEightHex()
	idx.Upsert(plugin)

	manager := &Manager{Dir: t.TempDir(), AllowUnsigned: true}
	if _, err := manager.Install(context.Background(), idx, "acme", ""); err == nil {
		t.Fatal("expected a checksum mismatch to be rejected")
	}
	if _, err := os.Stat(filepath.Join(manager.Dir, "acme")); !os.IsNotExist(err) {
		t.Fatalf("a rejected install must not leave content behind, stat err=%v", err)
	}
}

func TestManagerInstallRefusesUnsignedUnlessAllowed(t *testing.T) {
	repo := newTestRepo(t)
	tagRelease(t, repo, "v1.0.0", manifestFor("acme", "v1.0.0"), "plugin.py", "a")
	idx := syncedIndex(t, repo)

	manager := &Manager{Dir: t.TempDir()}
	if _, err := manager.Install(context.Background(), idx, "acme", ""); err == nil {
		t.Fatal("expected an unsigned manifest to be refused by default")
	}

	allowed := &Manager{Dir: t.TempDir(), AllowUnsigned: true}
	if _, err := allowed.Install(context.Background(), idx, "acme", ""); err != nil {
		t.Fatalf("AllowUnsigned should accept an unsigned manifest: %v", err)
	}
}

func TestManagerInstallVerifiesSignature(t *testing.T) {
	trustedPub, trustedPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	otherPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	manifest := Manifest{
		APIVersion: ManifestAPIVersion, Name: "acme", Version: "v1.0.0",
		Entrypoint: "plugin.py",
	}
	manifest.Sign(trustedPriv, "publisher-1")
	encoded, err := manifest.Encode()
	if err != nil {
		t.Fatal(err)
	}

	repo := newTestRepo(t)
	tagRelease(t, repo, "v1.0.0", string(encoded), "plugin.py", "a")
	idx := syncedIndex(t, repo)

	untrusted := &Manager{Dir: t.TempDir(), TrustedKeys: []ed25519.PublicKey{otherPub}}
	if _, err := untrusted.Install(context.Background(), idx, "acme", ""); err == nil {
		t.Fatal("expected installation to fail against an untrusted key set")
	}

	trusted := &Manager{Dir: t.TempDir(), TrustedKeys: []ed25519.PublicKey{trustedPub}}
	if _, err := trusted.Install(context.Background(), idx, "acme", ""); err != nil {
		t.Fatalf("expected installation to succeed against the trusted key: %v", err)
	}
}

func TestManagerInstallEnforcesHostCompatibility(t *testing.T) {
	manifest := Manifest{
		APIVersion: ManifestAPIVersion, Name: "acme", Version: "v1.0.0",
		Entrypoint: "plugin.py", Compatibility: []string{">=v2.0.0"},
	}
	encoded, err := manifest.Encode()
	if err != nil {
		t.Fatal(err)
	}
	repo := newTestRepo(t)
	tagRelease(t, repo, "v1.0.0", string(encoded), "plugin.py", "a")
	idx := syncedIndex(t, repo)

	incompatible := &Manager{Dir: t.TempDir(), AllowUnsigned: true, HostVersion: "v1.5.0"}
	if _, err := incompatible.Install(context.Background(), idx, "acme", ""); err == nil {
		t.Fatal("expected installation to be refused for an incompatible host version")
	}

	compatible := &Manager{Dir: t.TempDir(), AllowUnsigned: true, HostVersion: "v2.5.0"}
	if _, err := compatible.Install(context.Background(), idx, "acme", ""); err != nil {
		t.Fatalf("expected installation to succeed for a compatible host version: %v", err)
	}

	// A "dev" host build (not valid SemVer) must not block installs -
	// compatibility gating is skipped, not fail-closed, for that case.
	devBuild := &Manager{Dir: t.TempDir(), AllowUnsigned: true, HostVersion: "dev"}
	if _, err := devBuild.Install(context.Background(), idx, "acme", ""); err != nil {
		t.Fatalf("expected a dev host build to skip compatibility gating: %v", err)
	}
}

func TestManagerInstallUnknownPluginOrVersion(t *testing.T) {
	idx := &Index{}
	manager := &Manager{Dir: t.TempDir(), AllowUnsigned: true}
	if _, err := manager.Install(context.Background(), idx, "nope", ""); err == nil {
		t.Fatal("expected an error for a plugin absent from the index")
	}

	repo := newTestRepo(t)
	tagRelease(t, repo, "v1.0.0", manifestFor("acme", "v1.0.0"), "plugin.py", "a")
	synced := syncedIndex(t, repo)
	if _, err := manager.Install(context.Background(), synced, "acme", "v9.9.9"); err == nil {
		t.Fatal("expected an error for a version absent from the index")
	}
}

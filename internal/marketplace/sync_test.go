package marketplace

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncPromotesSignatureOnlyWithTrustedKey(t *testing.T) {
	repository := newTestRepo(t)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		APIVersion: ManifestAPIVersion, Name: "signed", Version: "v1.0.0",
		Entrypoint: "plugin.py",
	}
	manifest.Sign(privateKey, "publisher")
	encoded, err := manifest.Encode()
	if err != nil {
		t.Fatal(err)
	}
	tagRelease(t, repository, "v1.0.0", string(encoded), "plugin.py", "print('signed')")

	unsignedTrust, err := SyncSource(context.Background(), repository, PluginEntry{})
	if err != nil {
		t.Fatal(err)
	}
	release, _ := unsignedTrust.Plugin.Release("v1.0.0")
	if release.Verified {
		t.Fatal("signature presence alone must not mark a release verified")
	}

	trusted, err := SyncSourceWithKeys(context.Background(), repository, unsignedTrust.Plugin, []ed25519.PublicKey{publicKey})
	if err != nil {
		t.Fatal(err)
	}
	release, _ = trusted.Plugin.Release("v1.0.0")
	if !release.Verified {
		t.Fatal("trusted key should promote the existing release to verified")
	}
	if len(trusted.NewTags) != 0 {
		t.Fatalf("trust promotion is not a new tag: %v", trusted.NewTags)
	}
}

func manifestFor(name, version string) string {
	return "api_version: " + ManifestAPIVersion + "\n" +
		"name: " + name + "\n" +
		"version: " + version + "\n" +
		"description: a real test fixture plugin\n" +
		"author: Fixture Author\n" +
		"entrypoint: plugin.py\n"
}

func TestSyncSourceIndexesEveryTag(t *testing.T) {
	repo := newTestRepo(t)
	tagRelease(t, repo, "v1.0.0", manifestFor("acme", "v1.0.0"), "plugin.py", "print('v1')")
	tagRelease(t, repo, "v1.1.0", manifestFor("acme", "v1.1.0"), "plugin.py", "print('v1.1')")

	result, err := SyncSource(context.Background(), repo, PluginEntry{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SkippedTags) != 0 {
		t.Fatalf("unexpected skipped tags: %v", result.SkippedTags)
	}
	if len(result.Plugin.Releases) != 2 {
		t.Fatalf("want 2 releases, got %+v", result.Plugin.Releases)
	}
	if result.Plugin.LatestVersion != "v1.1.0" {
		t.Fatalf("latest version = %q, want v1.1.0", result.Plugin.LatestVersion)
	}
	if result.Plugin.Name != "acme" || result.Plugin.Description != "a real test fixture plugin" {
		t.Fatalf("unexpected plugin metadata: %+v", result.Plugin)
	}
	release, ok := result.Plugin.Release("v1.0.0")
	if !ok || !strings.HasPrefix(release.Checksum, "sha256:") {
		t.Fatalf("unexpected v1.0.0 release: %+v ok=%v", release, ok)
	}
	other, ok := result.Plugin.Release("v1.1.0")
	if !ok || other.Checksum == release.Checksum {
		t.Fatalf("different content must produce different checksums: %q vs %q", release.Checksum, other.Checksum)
	}
}

func TestSyncSourcePicksHighestSemverAsLatestRegardlessOfTagOrder(t *testing.T) {
	repo := newTestRepo(t)
	// Tag in a deliberately non-monotonic order: v0.9.0 committed after v2.0.0.
	tagRelease(t, repo, "v2.0.0", manifestFor("acme", "v2.0.0"), "plugin.py", "a")
	tagRelease(t, repo, "v0.9.0", manifestFor("acme", "v0.9.0"), "plugin.py", "b")

	result, err := SyncSource(context.Background(), repo, PluginEntry{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Plugin.LatestVersion != "v2.0.0" {
		t.Fatalf("latest version = %q, want v2.0.0", result.Plugin.LatestVersion)
	}
}

func TestSyncSourceIsIncremental(t *testing.T) {
	repo := newTestRepo(t)
	tagRelease(t, repo, "v1.0.0", manifestFor("acme", "v1.0.0"), "plugin.py", "a")

	first, err := SyncSource(context.Background(), repo, PluginEntry{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.NewTags) != 1 {
		t.Fatalf("first sync: want 1 new tag, got %v", first.NewTags)
	}

	// Nothing changed upstream: re-syncing against the previous result
	// must find zero new tags.
	second, err := SyncSource(context.Background(), repo, first.Plugin)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.NewTags) != 0 {
		t.Fatalf("second sync: want 0 new tags, got %v", second.NewTags)
	}
	if len(second.Plugin.Releases) != 1 {
		t.Fatalf("second sync: releases should be unchanged, got %+v", second.Plugin.Releases)
	}

	// A genuinely new tag is picked up incrementally.
	tagRelease(t, repo, "v1.1.0", manifestFor("acme", "v1.1.0"), "plugin.py", "b")
	third, err := SyncSource(context.Background(), repo, second.Plugin)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.NewTags) != 1 || third.NewTags[0] != "v1.1.0" {
		t.Fatalf("third sync: want exactly v1.1.0 as new, got %v", third.NewTags)
	}
	if len(third.Plugin.Releases) != 2 {
		t.Fatalf("third sync: want 2 releases total, got %+v", third.Plugin.Releases)
	}
}

func TestSyncSourceSkipsInvalidTagWithoutFailingTheWholeSync(t *testing.T) {
	repo := newTestRepo(t)
	tagRelease(t, repo, "v1.0.0", manifestFor("acme", "v1.0.0"), "plugin.py", "good")
	// A tag whose manifest declares a mismatched version.
	tagRelease(t, repo, "v2.0.0", manifestFor("acme", "v9.9.9"), "plugin.py", "mismatch")

	result, err := SyncSource(context.Background(), repo, PluginEntry{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Plugin.Releases) != 1 || result.Plugin.Releases[0].Version != "v1.0.0" {
		t.Fatalf("want only v1.0.0 indexed, got %+v", result.Plugin.Releases)
	}
	if _, skipped := result.SkippedTags["v2.0.0"]; !skipped {
		t.Fatalf("want v2.0.0 recorded as skipped, got %v", result.SkippedTags)
	}
}

func TestSyncSourceRejectsEmptyRepository(t *testing.T) {
	if _, err := SyncSource(context.Background(), "", PluginEntry{}); err == nil {
		t.Fatal("expected an error for an empty repository URL")
	}
}

func TestHashEntrypointRejectsAMissingPath(t *testing.T) {
	if _, err := hashEntrypoint(t.TempDir(), "missing.py"); err == nil {
		t.Fatal("expected an error for a missing entrypoint")
	}
}

func TestHashEntrypointRegularFileIsDeterministic(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "plugin.py"), []byte("print('hi')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := hashEntrypoint(root, "plugin.py")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("hash=%q", first)
	}
	second, err := hashEntrypoint(root, "plugin.py")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("expected a deterministic hash, got %q then %q", first, second)
	}

	if err := os.WriteFile(filepath.Join(root, "plugin.py"), []byte("print('changed')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := hashEntrypoint(root, "plugin.py")
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("expected the hash to change when the file content changes")
	}
}

func TestHashEntrypointDirectoryIsDeterministicAndOrderIndependent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "plugin")
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.py"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "b.py"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A .git directory anywhere in the tree must never affect the hash.
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := hashEntrypoint(root, "plugin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("hash=%q", first)
	}

	// Rebuild the same tree in a different creation order; the walk
	// itself sorts relative paths before hashing, so the result must be
	// identical regardless of directory iteration order.
	root2 := t.TempDir()
	dir2 := filepath.Join(root2, "plugin")
	if err := os.MkdirAll(filepath.Join(dir2, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "nested", "b.py"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "a.py"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := hashEntrypoint(root2, "plugin")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("expected identical hashes for identical content, got %q and %q", first, second)
	}
}

package plugin

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifiedExecutableSnapshotValidation(t *testing.T) {
	root := t.TempDir()
	if _, _, err := verifiedExecutableSnapshot(root, Manifest{Executable: "missing", Digest: "sha256:none"}); err == nil {
		t.Fatal("missing executable accepted")
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifiedExecutableSnapshot(root, Manifest{Executable: "directory", Digest: "sha256:none"}); err == nil {
		t.Fatal("directory executable accepted")
	}
	path := filepath.Join(root, "plugin")
	content := []byte("verified executable")
	if err := os.WriteFile(path, content, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifiedExecutableSnapshot(root, Manifest{Executable: "plugin", Digest: "sha256:wrong"}); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	digest := sha256.Sum256(content)
	snapshot, cleanup, err := verifiedExecutableSnapshot(root, Manifest{Executable: "plugin", Digest: "sha256:" + hex.EncodeToString(digest[:])})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(snapshot); err != nil || string(got) != string(content) {
		t.Fatalf("snapshot = %q, %v", got, err)
	}
	cleanup()
	if _, err := os.Stat(snapshot); !os.IsNotExist(err) {
		t.Fatalf("snapshot cleanup error = %v", err)
	}
}

func TestVerifiedExecutableSnapshotRejectsSymlinkAndSymlinkParent(t *testing.T) {
	root := t.TempDir()
	payload := []byte("plugin")
	digest := sha256.Sum256(payload)
	wantDigest := "sha256:" + hex.EncodeToString(digest[:])
	realFile := filepath.Join(root, "real-plugin")
	if err := os.WriteFile(realFile, payload, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real-plugin", filepath.Join(root, "plugin-link")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifiedExecutableSnapshot(root, Manifest{Executable: "plugin-link", Digest: wantDigest}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("direct symlink accepted: %v", err)
	}
	realDir := filepath.Join(root, "real-dir")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "plugin"), payload, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real-dir", filepath.Join(root, "dir-link")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifiedExecutableSnapshot(root, Manifest{Executable: "dir-link/plugin", Digest: wantDigest}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink parent accepted: %v", err)
	}
}

func TestVerifiedExecutableSnapshotResistsReplacementAfterRootIsPinned(t *testing.T) {
	payload := []byte("trusted plugin")
	digest := sha256.Sum256(payload)
	wantDigest := "sha256:" + hex.EncodeToString(digest[:])
	t.Run("parent replaced by symlink", func(t *testing.T) {
		root := t.TempDir()
		parent := filepath.Join(root, "bin")
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(parent, "plugin"), payload, 0o700); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "plugin"), payload, 0o700); err != nil {
			t.Fatal(err)
		}
		originalHook := afterPluginRootOpen
		afterPluginRootOpen = func() {
			if err := os.Rename(parent, filepath.Join(root, "original-bin")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, parent); err != nil {
				t.Fatal(err)
			}
		}
		defer func() { afterPluginRootOpen = originalHook }()
		if _, _, err := verifiedExecutableSnapshot(root, Manifest{Executable: "bin/plugin", Digest: wantDigest}); err == nil {
			t.Fatal("replacement parent symlink was followed")
		}
	})
	t.Run("file replaced by symlink", func(t *testing.T) {
		root := t.TempDir()
		file := filepath.Join(root, "plugin")
		if err := os.WriteFile(file, payload, 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "plugin")
		if err := os.WriteFile(outside, payload, 0o700); err != nil {
			t.Fatal(err)
		}
		originalHook := afterPluginRootOpen
		afterPluginRootOpen = func() {
			if err := os.Rename(file, filepath.Join(root, "original-plugin")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, file); err != nil {
				t.Fatal(err)
			}
		}
		defer func() { afterPluginRootOpen = originalHook }()
		if _, _, err := verifiedExecutableSnapshot(root, Manifest{Executable: "plugin", Digest: wantDigest}); err == nil {
			t.Fatal("replacement file symlink was followed")
		}
	})
}

func TestTrustPolicyRejectsRevokedKeyAfterRelabelAndResign(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{APIVersion: ManifestAPIVersion, Name: "plugin", Version: "v1", Capabilities: []string{"detect"}, Executable: "plugin", Digest: "sha256:" + strings.Repeat("a", 64)}
	if err := manifest.Sign(private, "compromised"); err != nil {
		t.Fatal(err)
	}
	policy := TrustPolicy{TrustedKeys: map[string]ed25519.PublicKey{"compromised": public}, RevokedKeyIDs: []string{"compromised"}}
	if !policy.IsRevoked(manifest) {
		t.Fatal("original key label was not revoked")
	}
	if err := manifest.Sign(private, "fresh-label"); err != nil {
		t.Fatal(err)
	}
	if err := policy.verifySignature(manifest); err == nil {
		t.Fatal("same compromised key bypassed revocation by relabeling")
	}
	policy = TrustPolicy{Keys: []ed25519.PublicKey{public}, RevokedKeyDigests: []string{publicKeyDigest(public)}}
	if err := policy.verifySignature(manifest); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("key fingerprint revocation bypassed: %v", err)
	}
}

func TestVerifyStartAndPublishAvailableRequiresRegistry(t *testing.T) {
	_, err := VerifyStartAndPublishAvailable(context.Background(), nil, t.TempDir(), Manifest{Name: "missing-registry"}, TrustPolicy{})
	if err == nil || !strings.Contains(err.Error(), "registry is required") {
		t.Fatalf("error = %v", err)
	}
}

func testManifest(t *testing.T, dir string, executable []byte) Manifest {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "plugin-binary"), executable, 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(executable)
	return Manifest{
		APIVersion: ManifestAPIVersion, Name: "example-plugin", Version: "v1.0.0",
		Capabilities: []string{"detect"}, Executable: "plugin-binary",
		Digest: "sha256:" + hex.EncodeToString(digest[:]),
	}
}

func writeManifest(t *testing.T, dir string, manifest Manifest) {
	t.Helper()
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadManifestRoundTripAndExecutableVerification(t *testing.T) {
	dir := t.TempDir()
	manifest := testManifest(t, dir, []byte("plugin payload"))
	writeManifest(t, dir, manifest)
	loaded, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "example-plugin" || loaded.Digest != manifest.Digest {
		t.Fatalf("loaded=%+v", loaded)
	}
	if err := loaded.VerifyExecutable(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin-binary"), []byte("tampered payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := loaded.VerifyExecutable(dir); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered executable accepted: %v", err)
	}
}

func TestLoadManifestAndExecutableFailClosedOnFilesystemAndJSONErrors(t *testing.T) {
	if _, err := LoadManifest(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing manifest accepted")
	}
	for name, content := range map[string]string{
		"malformed": `{`,
		"unknown":   `{"api_version":"secure-oci.dev/plugin-manifest/v1","extra":true}`,
		"trailing":  `{} {}`,
		"invalid":   `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ManifestFileName), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadManifest(dir); err == nil {
				t.Fatalf("%s manifest accepted", name)
			}
		})
	}

	base := Manifest{
		APIVersion: ManifestAPIVersion, Name: "example", Version: "v1",
		Capabilities: []string{"detect"}, Executable: "plugin-binary",
		Digest: "sha256:" + strings.Repeat("0", 64),
	}
	if err := base.VerifyExecutable(t.TempDir()); err == nil {
		t.Fatal("missing executable accepted")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, base.Executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := base.VerifyExecutable(dir); err == nil {
		t.Fatal("directory executable accepted")
	}
}

func TestManifestValidationRejectsUnsafeFields(t *testing.T) {
	base := Manifest{
		APIVersion: ManifestAPIVersion, Name: "ok", Version: "v1",
		Capabilities: []string{"detect"}, Executable: "bin",
		Digest: "sha256:" + strings.Repeat("a", 64),
	}
	mutations := map[string]func(*Manifest){
		"api-version":          func(m *Manifest) { m.APIVersion = "v2" },
		"name":                 func(m *Manifest) { m.Name = "Bad Name" },
		"version":              func(m *Manifest) { m.Version = "" },
		"no-capabilities":      func(m *Manifest) { m.Capabilities = nil },
		"bad-capability":       func(m *Manifest) { m.Capabilities = []string{"Detect!"} },
		"duplicate-capability": func(m *Manifest) { m.Capabilities = []string{"detect", "detect"} },
		"platform":             func(m *Manifest) { m.Platforms = []string{"windows/amd64"} },
		"absolute-executable":  func(m *Manifest) { m.Executable = "/bin/sh" },
		"traversal-executable": func(m *Manifest) { m.Executable = "../escape" },
		"digest":               func(m *Manifest) { m.Digest = "sha256:short" },
		"signature-algorithm": func(m *Manifest) {
			m.Signature = &ManifestSignature{Algorithm: "rsa", Value: "AA=="}
		},
		"signature-value": func(m *Manifest) {
			m.Signature = &ManifestSignature{Algorithm: "ed25519", Value: "not base64!"}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			manifest := base
			mutate(&manifest)
			if err := manifest.Validate(); err == nil {
				t.Fatalf("invalid manifest accepted: %+v", manifest)
			}
		})
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
}

func TestManifestSignAndVerify(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	manifest := testManifest(t, dir, []byte("signed payload"))
	if err := manifest.Sign(private, "release-key"); err != nil {
		t.Fatal(err)
	}
	if err := manifest.VerifySignature([]ed25519.PublicKey{public}); err != nil {
		t.Fatal(err)
	}
	if err := manifest.VerifySignature([]ed25519.PublicKey{otherPublic}); err == nil {
		t.Fatal("signature verified against the wrong key")
	}
	tampered := manifest
	tampered.Version = "v9.9.9"
	if err := tampered.VerifySignature([]ed25519.PublicKey{public}); err == nil {
		t.Fatal("tampered manifest still verifies")
	}
	unsigned := manifest
	unsigned.Signature = nil
	if err := unsigned.VerifySignature([]ed25519.PublicKey{public}); err == nil {
		t.Fatal("unsigned manifest verified")
	}
}

func TestDiscoverFindsPluginsInDeterministicOrder(t *testing.T) {
	if _, err := Discover(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing discovery root accepted")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ordinary-file"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"zeta", "alpha"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := testManifest(t, dir, []byte("payload "+name))
		manifest.Name = name
		writeManifest(t, dir, manifest)
	}
	if err := os.MkdirAll(filepath.Join(root, "not-a-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	discovered, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered) != 2 || discovered[0].Manifest.Name != "alpha" || discovered[1].Manifest.Name != "zeta" {
		t.Fatalf("discovered=%+v", discovered)
	}
	duplicate := filepath.Join(root, "alpha-copy")
	if err := os.MkdirAll(duplicate, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := testManifest(t, duplicate, []byte("payload alpha"))
	manifest.Name = "alpha"
	writeManifest(t, duplicate, manifest)
	if _, err := Discover(root); err == nil || !strings.Contains(err.Error(), "appears in both") {
		t.Fatalf("duplicate names accepted: %v", err)
	}
}

func TestLoadPublicKeyRejectsNonEd25519Input(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(filename, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	key, err := LoadPublicKey(filename)
	if err != nil || len(key) != ed25519.PublicKeySize {
		t.Fatalf("key=%v err=%v", key, err)
	}
	bad := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(bad, []byte("not a key"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPublicKey(bad); err == nil {
		t.Fatal("malformed key accepted")
	}
	if _, err := LoadPublicKey(filepath.Join(t.TempDir(), "missing.pem")); err == nil {
		t.Fatal("missing key accepted")
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaDER, err := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	rsaFile := filepath.Join(t.TempDir(), "rsa.pem")
	if err := os.WriteFile(rsaFile, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: rsaDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPublicKey(rsaFile); err == nil {
		t.Fatal("RSA key accepted as Ed25519")
	}
}

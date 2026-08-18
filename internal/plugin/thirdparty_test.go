package plugin

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	appmigration "github.com/CYPT71/platform-factory/internal/app/migration"
)

// thirdPartyPluginPath builds testdata/thirdparty — a separate Go module
// that imports only the public sdk/plugin SDK — with the module proxy
// disabled, proving the plugin builds out of tree, offline, without the
// secure-oci core being recompiled or its internals exposed.
var thirdPartyPluginPath = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "platform-factory-zig-plugin-*")
	if err != nil {
		return "", err
	}
	binary := filepath.Join(dir, "zig-adapter")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = filepath.Join("..", "..", "testdata", "plugins", "thirdparty")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build third-party plugin: %w: %s", err, output)
	}
	return binary, nil
})

func installThirdPartyPlugin(t *testing.T, sign bool) (string, ed25519.PublicKey) {
	t.Helper()
	binary, err := thirdPartyPluginPath()
	if err != nil {
		t.Fatalf("build third-party plugin: %v", err)
	}
	payload, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "zig-adapter")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "zig-adapter"), payload, 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	manifest := Manifest{
		APIVersion: ManifestAPIVersion, Name: "zig-adapter", Version: "v0.1.0",
		Capabilities: []string{"detect", "freeze", "plan"}, Executable: "zig-adapter",
		Digest: "sha256:" + hex.EncodeToString(digest[:]),
	}
	var public ed25519.PublicKey
	if sign {
		generated, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		public = generated
		if err := manifest.Sign(private, "test-key"); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest(t, dir, manifest)
	return root, public
}

func TestThirdPartyPluginAddsLanguageWithoutRecompilingTheHost(t *testing.T) {
	root, public := installThirdPartyPlugin(t, true)
	discovered, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered) != 1 || discovered[0].Manifest.Name != "zig-adapter" {
		t.Fatalf("discovered=%+v", discovered)
	}
	client, err := VerifyAndStart(context.Background(), discovered[0].Dir, discovered[0].Manifest,
		TrustPolicy{Keys: []ed25519.PublicKey{public}, AllowUnsandboxedExecution: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "build.zig"), []byte("const std = @import(\"std\");"), 0o644); err != nil {
		t.Fatal(err)
	}
	var detected struct {
		Kind    string `json:"kind"`
		Profile string `json:"profile"`
	}
	if err := client.Call(context.Background(), "v1.detect", struct {
		Path string `json:"path"`
	}{Path: project}, &detected); err != nil {
		t.Fatal(err)
	}
	if detected.Kind != "zig" || detected.Profile != "static" {
		t.Fatalf("detected=%+v", detected)
	}
	var frozen struct {
		Steps [][]string `json:"steps"`
	}
	if err := client.Call(context.Background(), "v1.freeze", struct {
		Language string `json:"language"`
		Root     string `json:"root"`
	}{Language: "zig", Root: project}, &frozen); err != nil {
		t.Fatal(err)
	}
	if len(frozen.Steps) != 1 || strings.Join(frozen.Steps[0], " ") != "zig build --fetch" {
		t.Fatalf("frozen=%+v", frozen)
	}
}

func TestVerifyStartAndPublishAvailablePublishesVerifiedEvidence(t *testing.T) {
	root, public := installThirdPartyPlugin(t, true)
	registry, err := DiscoverAndRegister(root)
	if err != nil {
		t.Fatal(err)
	}
	discovered, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	client, err := VerifyStartAndPublishAvailable(context.Background(), registry, discovered[0].Dir, discovered[0].Manifest, TrustPolicy{Keys: []ed25519.PublicKey{public}, AllowUnsandboxedExecution: true})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := registry.Candidates(context.Background(), appmigration.CapabilityRequirement{Capability: "detect"})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || !candidates[0].Evidence.Available || !candidates[0].Evidence.Verified || !candidates[0].Evidence.Negotiated {
		t.Fatalf("verified evidence not published: %+v", candidates)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	candidates, err = registry.Candidates(context.Background(), appmigration.CapabilityRequirement{Capability: "detect"})
	if err != nil {
		t.Fatal(err)
	}
	if candidates[0].Evidence.Available {
		t.Fatal("closed client remained available")
	}
}

func TestVerifyAndStartRefusesUnsignedAndTamperedPlugins(t *testing.T) {
	unsignedRoot, _ := installThirdPartyPlugin(t, false)
	unsigned, err := Discover(unsignedRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAndStart(context.Background(), unsigned[0].Dir, unsigned[0].Manifest, TrustPolicy{}); err == nil ||
		!strings.Contains(err.Error(), "unsigned") {
		t.Fatalf("unsigned plugin accepted: %v", err)
	}
	client, err := VerifyAndStart(context.Background(), unsigned[0].Dir, unsigned[0].Manifest,
		TrustPolicy{AllowUnsigned: true, AllowUnsandboxedExecution: true})
	if err != nil {
		t.Fatalf("explicit unsigned override rejected: %v", err)
	}
	_ = client.Close()

	signedRoot, public := installThirdPartyPlugin(t, true)
	signed, err := Discover(signedRoot)
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(signed[0].Dir, "zig-adapter")
	payload, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)-1] ^= 0xff
	if err := os.WriteFile(executable, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAndStart(context.Background(), signed[0].Dir, signed[0].Manifest,
		TrustPolicy{Keys: []ed25519.PublicKey{public}}); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered executable accepted: %v", err)
	}
	if _, err := VerifyAndStart(context.Background(), signed[0].Dir, signed[0].Manifest,
		TrustPolicy{AllowUnsigned: true}); err == nil {
		t.Fatal("digest pin must hold even with the unsigned override")
	}
}

func TestVerifyAndStartRefusesUnavailableSandboxByDefault(t *testing.T) {
	root, public := installThirdPartyPlugin(t, true)
	discovered, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	original := pluginSandboxWrapper
	pluginSandboxWrapper = func(*exec.Cmd, PluginFamily, PluginPermissions) error {
		return errors.New("sandbox unavailable (stub)")
	}
	defer func() { pluginSandboxWrapper = original }()
	if client, err := VerifyAndStart(context.Background(), discovered[0].Dir, discovered[0].Manifest,
		TrustPolicy{Keys: []ed25519.PublicKey{public}}); err == nil {
		_ = client.Close()
		t.Fatal("trusted plugin launched without required isolation")
	} else if !strings.Contains(err.Error(), "required sandbox unavailable") {
		t.Fatalf("unexpected refusal: %v", err)
	}
}

// TestVerifyAndStartRefusesARevokedPlugin verifies that revocation takes
// precedence over an otherwise valid signature.
func TestVerifyAndStartRefusesARevokedPlugin(t *testing.T) {
	root, public := installThirdPartyPlugin(t, true)
	discovered, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest := discovered[0].Manifest

	// Sanity: this exact manifest+policy combination would otherwise be
	// accepted - proves the revocation check below is what's rejecting
	// it, not some unrelated failure.
	client, err := VerifyAndStart(context.Background(), discovered[0].Dir, manifest, TrustPolicy{Keys: []ed25519.PublicKey{public}, AllowUnsandboxedExecution: true})
	if err != nil {
		t.Fatalf("setup: expected this plugin to start without revocation, got: %v", err)
	}
	_ = client.Close()

	if _, err := VerifyAndStart(context.Background(), discovered[0].Dir, manifest,
		TrustPolicy{Keys: []ed25519.PublicKey{public}, RevokedDigests: []string{manifest.Digest}}); err == nil ||
		!strings.Contains(err.Error(), "revoked") {
		t.Fatalf("plugin with a revoked digest was accepted: %v", err)
	}
	if _, err := VerifyAndStart(context.Background(), discovered[0].Dir, manifest,
		TrustPolicy{Keys: []ed25519.PublicKey{public}, RevokedKeyIDs: []string{manifest.Signature.KeyID}}); err == nil ||
		!strings.Contains(err.Error(), "revoked") {
		t.Fatalf("plugin signed by a revoked key was accepted: %v", err)
	}
}

func TestVerifyAndStartRejectsHandshakeMismatch(t *testing.T) {
	root, public := installThirdPartyPlugin(t, true)
	discovered, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	// Re-sign a manifest that pins the right executable but claims a
	// different identity than the plugin reports at v1.hello.
	generated, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := discovered[0].Manifest
	manifest.Version = "v9.9.9"
	if err := manifest.Sign(private, "other-key"); err != nil {
		t.Fatal(err)
	}
	_ = public
	if _, err := VerifyAndStart(context.Background(), discovered[0].Dir, manifest,
		TrustPolicy{Keys: []ed25519.PublicKey{generated}, AllowUnsandboxedExecution: true}); err == nil ||
		!strings.Contains(err.Error(), "does not match the manifest") {
		t.Fatalf("handshake mismatch accepted: %v", err)
	}
}

func TestVerifyAndStartExecutesVerifiedSnapshotAfterPathSubstitution(t *testing.T) {
	root, public := installThirdPartyPlugin(t, true)
	discovered, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(discovered[0].Dir, discovered[0].Manifest.Executable)
	originalHook := afterVerifiedExecutableSnapshot
	afterVerifiedExecutableSnapshot = func() {
		if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 99\n"), 0o755); err != nil {
			t.Errorf("substitute executable: %v", err)
		}
	}
	defer func() { afterVerifiedExecutableSnapshot = originalHook }()
	client, err := VerifyAndStart(context.Background(), discovered[0].Dir, discovered[0].Manifest,
		TrustPolicy{Keys: []ed25519.PublicKey{public}, AllowUnsandboxedExecution: true})
	if err != nil {
		t.Fatalf("verified snapshot was replaced through original path: %v", err)
	}
	defer client.Close()
	if client.Hello().Name != discovered[0].Manifest.Name {
		t.Fatalf("unexpected executable identity: %+v", client.Hello())
	}
}

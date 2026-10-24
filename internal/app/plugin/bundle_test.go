package plugin

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	hostplugin "github.com/CYPT71/platform-factory/internal/plugin"
	"github.com/CYPT71/platform-factory/internal/signing"
)

func bundleFixture(t *testing.T) BundleOptions {
	t.Helper()
	source := t.TempDir()
	manifest := hostplugin.Manifest{APIVersion: hostplugin.ManifestAPIVersion, Name: "example", Version: "1.0.0", Family: hostplugin.PluginFamilyDeployment, Capabilities: []string{"deployment.apply"}, Executable: "old-name", Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000"}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "example-plugin")
	// Deliberately not executable code: packaging/verifying must never run it.
	if err := os.WriteFile(binary, []byte("plugin artifact bytes"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := signing.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return BundleOptions{Source: source, Executable: binary, Output: filepath.Join(t.TempDir(), "bundle"), KeyName: "publisher", Store: store}
}

func TestBuildBundleSignedRoundTripAndInstall(t *testing.T) {
	options := bundleFixture(t)
	manifest, err := BuildBundle(options)
	if err != nil {
		t.Fatal(err)
	}
	public, err := hostplugin.LoadPublicKey(filepath.Join(options.Output, "publisher.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Executable != "example-plugin" || manifest.Signature == nil {
		t.Fatalf("manifest=%+v", manifest)
	}
	if _, err := VerifyBundle(options.Output, []ed25519.PublicKey{public}, false); err != nil {
		t.Fatal(err)
	}
	registry := t.TempDir()
	if err := InstallPluginBundle(registry, options.Output, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBundle(filepath.Join(registry, "example"), []ed25519.PublicKey{public}, false); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(options.Output)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("unexpected exported files %v", files)
	}
	original, err := os.ReadFile(filepath.Join(options.Output, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildBundle(options); err == nil {
		t.Fatal("overwrote published bundle")
	}
	after, err := os.ReadFile(filepath.Join(options.Output, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != string(after) {
		t.Fatal("existing bundle modified")
	}
}

func TestVerifyBundleRefusesTamperingAndUntrustedSigners(t *testing.T) {
	for _, kind := range []string{"missing-key", "wrong-key", "executable", "metadata", "unsigned"} {
		t.Run(kind, func(t *testing.T) {
			options := bundleFixture(t)
			manifest, err := BuildBundle(options)
			if err != nil {
				t.Fatal(err)
			}
			public, err := options.Store.PublicKey(options.KeyName)
			if err != nil {
				t.Fatal(err)
			}
			keys := []ed25519.PublicKey{public}
			switch kind {
			case "missing-key":
				keys = nil
			case "wrong-key":
				key, _, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				keys = []ed25519.PublicKey{key}
			case "executable":
				if err := os.WriteFile(filepath.Join(options.Output, manifest.Executable), []byte("tampered"), 0700); err != nil {
					t.Fatal(err)
				}
			case "metadata":
				manifest.Permissions.Secrets = []string{"kubeconfig"}
			case "unsigned":
				manifest.Signature = nil
			}
			if kind == "metadata" || kind == "unsigned" {
				data, err := json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(options.Output, "plugin.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := VerifyBundle(options.Output, keys, false); err == nil {
				t.Fatal("accepted", kind)
			}
			if kind != "unsigned" {
				if _, err := VerifyBundle(options.Output, keys, true); err == nil {
					t.Fatal("allow-unsigned bypassed signature/digest")
				}
			} else if _, err := VerifyBundle(options.Output, keys, true); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type failingBundleSigner struct{ signing.KeyStore }

func (s failingBundleSigner) Sign(string, []byte) ([]byte, error) {
	return nil, errors.New("signer unavailable")
}

func TestBuildBundleCleansUpFailedSigningAndRejectsSymlinks(t *testing.T) {
	options := bundleFixture(t)
	options.Store = failingBundleSigner{options.Store}
	if _, err := BuildBundle(options); err == nil {
		t.Fatal("accepted signing failure")
	}
	if _, err := os.Lstat(options.Output); !os.IsNotExist(err) {
		t.Fatalf("published incomplete bundle: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(options.Output))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("temporary files leaked")
	}
	options = bundleFixture(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(options.Executable, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	options.Executable = link
	if _, err := BuildBundle(options); err == nil {
		t.Fatal("accepted symlink executable")
	}
}

func TestConcurrentBundlePublishHasOneWinner(t *testing.T) {
	options := bundleFixture(t)
	if _, err := options.Store.PublicKey(options.KeyName); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() { defer group.Done(); _, err := BuildBundle(options); results <- err }()
	}
	group.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful publishers=%d", successes)
	}
	public, err := options.Store.PublicKey(options.KeyName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBundle(options.Output, []ed25519.PublicKey{public}, false); err != nil {
		t.Fatal(err)
	}
}

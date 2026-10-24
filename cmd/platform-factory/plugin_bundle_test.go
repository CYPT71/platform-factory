package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	pluginapp "github.com/CYPT71/platform-factory/internal/app/plugin"
	"os"
	"path/filepath"
	"testing"
	"time"

	hostplugin "github.com/CYPT71/platform-factory/internal/plugin"
)

func TestPluginBundleInstallsRealOfficialPlugins(t *testing.T) {
	for _, test := range []struct {
		name, capability string
		build            func() (string, error)
	}{{"kubernetes", "deployment.apply", kubernetesPluginBinary}, {"kubevirt", "runtime.create", kubevirtPluginBinary}} {
		t.Run(test.name, func(t *testing.T) {
			binary, err := test.build()
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			bundle := filepath.Join(root, "bundle")
			keys := filepath.Join(root, "private-keys")
			registry := filepath.Join(root, "registry")
			var out, diagnostics bytes.Buffer
			args := []string{"bundle", "--from", filepath.Join("..", "..", "plugins", test.name), "--executable", binary, "--output", bundle, "--key-dir", keys, "--format", "json"}
			if code := runPlugin(args, &out, &diagnostics); code != 0 {
				t.Fatalf("bundle=%d: %s", code, &diagnostics)
			}
			var result struct {
				Signed      bool   `json:"signed"`
				Fingerprint string `json:"publisher_fingerprint"`
			}
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if !result.Signed || result.Fingerprint == "" {
				t.Fatalf("result=%s", &out)
			}
			key := filepath.Join(bundle, "publisher.pem")
			out.Reset()
			diagnostics.Reset()
			if code := runPlugin([]string{"verify", "--from", bundle, "--key", key, "--format", "json"}, &out, &diagnostics); code != 0 {
				t.Fatalf("verify=%d: %s", code, &diagnostics)
			}
			if code := runPlugin([]string{"install", "--from", bundle, "--plugin-dir", registry}, &out, &diagnostics); code != 1 {
				t.Fatalf("install without trust=%d", code)
			}
			diagnostics.Reset()
			if code := runPlugin([]string{"install", "--from", bundle, "--plugin-dir", registry, "--key", key}, &out, &diagnostics); code != 0 {
				t.Fatalf("install=%d: %s", code, &diagnostics)
			}
			installed := filepath.Join(registry, test.name)
			manifest, err := hostplugin.LoadManifest(installed)
			if err != nil {
				t.Fatal(err)
			}
			public, err := hostplugin.LoadPublicKey(key)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			// Explicit test-only sandbox opt-out allows the same handshake on macOS
			// and unprivileged Linux CI. No cluster method is called.
			client, err := hostplugin.VerifyAndStart(ctx, installed, manifest, hostplugin.TrustPolicy{Keys: []ed25519.PublicKey{public}, AllowUnsandboxedExecution: true})
			if err != nil {
				t.Fatal(err)
			}
			if !client.HasCapability(test.capability) {
				client.Close()
				t.Fatal("missing negotiated capability", test.capability)
			}
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(installed, manifest.Executable), []byte("altered"), 0700); err != nil {
				t.Fatal(err)
			}
			diagnostics.Reset()
			if code := runPlugin([]string{"verify", "--from", installed, "--key", key}, &out, &diagnostics); code != 1 {
				t.Fatal("accepted altered installed executable")
			}
		})
	}
}

func TestPluginBundleRejectsPrivateKeysInsideDistribution(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "bundle")
	var out, diagnostics bytes.Buffer
	args := []string{"bundle", "--from", "unused", "--executable", "unused", "--output", output, "--key-dir", filepath.Join(output, "private")}
	if code := runPlugin(args, &out, &diagnostics); code != 2 {
		t.Fatalf("code=%d: %s", code, &diagnostics)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("created output/private keys: %v", err)
	}
	// A sibling symlink to the output's parent must not bypass containment.
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	args[len(args)-1] = filepath.Join(alias, "bundle", "private")
	diagnostics.Reset()
	if code := runPlugin(args, &out, &diagnostics); code != 2 {
		t.Fatalf("symlink containment bypass: %d %s", code, &diagnostics)
	}
}

func TestPluginHostDiscoversManagedInstallDirectory(t *testing.T) {
	// Isolate the OS-specific config directory for all supported hosts.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("AppData", home)
	t.Setenv("PLATFORM_FACTORY_PLUGIN_DIR", "")
	root, err := pluginapp.GenericPluginDir("")
	if err != nil {
		t.Fatal(err)
	}
	empty := registerPluginFlags(flag.NewFlagSet("empty", flag.ContinueOnError))
	if empty.dir != "" {
		t.Fatalf("fresh install unexpectedly discovers %s", empty.dir)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	installed := registerPluginFlags(flag.NewFlagSet("installed", flag.ContinueOnError))
	if installed.dir != root {
		t.Fatalf("default discovery=%s want %s", installed.dir, root)
	}
	override := t.TempDir()
	flags := flag.NewFlagSet("override", flag.ContinueOnError)
	options := registerPluginFlags(flags)
	if err := flags.Parse([]string{"--plugin-dir", override}); err != nil {
		t.Fatal(err)
	}
	if options.dir != override {
		t.Fatalf("explicit override lost: %s", options.dir)
	}
}

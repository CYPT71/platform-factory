package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"github.com/CYPT71/platform-factory/internal/extensions"
	hostplugin "github.com/CYPT71/platform-factory/internal/plugin"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestExtensionsCLIAndFiftySignedExecutableBundles(t *testing.T) {
	t.Setenv("PLATFORM_FACTORY_HOOKS_CONFIG", "")
	root := t.TempDir()
	toolkit := filepath.Join(root, "toolkit")
	if runtime.GOOS == "windows" {
		toolkit += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", toolkit, "../platform-factory-toolkit")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, raw)
	}
	var out, diagnostics bytes.Buffer
	if code := run([]string{"extensions", "list"}, &out, &diagnostics); code != 0 {
		t.Fatalf("list=%d %s", code, &diagnostics)
	}
	var rows []extensions.Profile
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 50 {
		t.Fatalf("catalog: %v, size %d", err, len(rows))
	}
	bundle := filepath.Join(root, "bundles")
	if code := runExtensions([]string{"bundle", "--executable", toolkit, "--output", bundle, "--key-dir", filepath.Join(root, "keys")}, &out, &diagnostics); code != 0 {
		t.Fatalf("bundle=%d %s", code, &diagnostics)
	}
	for _, p := range extensions.Catalog() {
		t.Run(p.Name, func(t *testing.T) {
			dir := filepath.Join(bundle, p.Name)
			manifest, err := hostplugin.LoadManifest(dir)
			if err != nil {
				t.Fatal(err)
			}
			key, err := hostplugin.LoadPublicKey(filepath.Join(dir, "publisher.pem"))
			if err != nil {
				t.Fatal(err)
			}
			if err := manifest.VerifySignature([]ed25519.PublicKey{key}); err != nil {
				t.Fatal(err)
			}
			if err := manifest.VerifyExecutable(dir); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client, err := hostplugin.VerifyAndStart(ctx, dir, manifest, hostplugin.TrustPolicy{Keys: []ed25519.PublicKey{key}, AllowUnsandboxedExecution: true})
			if err != nil {
				t.Fatal(err)
			}
			if !client.HasCapability("analyzer.scan") {
				client.Close()
				t.Fatal("missing analyzer contract")
			}
			var remote extensions.Report
			if err := client.Call(ctx, "v1.analyzer.scan", map[string]any{"document": p.Example}, &remote); err != nil || !remote.Passed {
				client.Close()
				t.Fatalf("real RPC check: %v %+v", err, remote)
			}
			if err := client.Call(ctx, "v1.analyzer.scan", map[string]any{"document": p.RejectedExample}, &remote); err != nil || remote.Passed {
				client.Close()
				t.Fatalf("real RPC rejection: %v %+v", err, remote)
			}
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, "input.json")
			raw, _ := json.Marshal(p.Example)
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			out.Reset()
			diagnostics.Reset()
			if code := runExtensions([]string{"analyze", p.Name, "--input", file}, &out, &diagnostics); code != 0 {
				t.Fatalf("good=%d %s", code, &diagnostics)
			}
			raw, _ = json.Marshal(p.RejectedExample)
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if code := runExtensions([]string{"analyze", p.Name, "--input", file}, &out, &diagnostics); code != 1 {
				t.Fatalf("bad=%d %s", code, &diagnostics)
			}
		})
	}
	// Never overwrite a published distribution, or put private keys inside it.
	for _, args := range [][]string{{"bundle", "--executable", toolkit, "--output", bundle, "--key-dir", filepath.Join(root, "keys")}, {"bundle", "--executable", toolkit, "--output", filepath.Join(root, "bad"), "--key-dir", filepath.Join(root, "bad", "private")}} {
		if code := runExtensions(args, &out, &diagnostics); code == 0 {
			t.Fatal("unsafe bundle accepted")
		}
	}
}
func TestExtensionsUsageAndErrors(t *testing.T) {
	var out, diagnostics bytes.Buffer
	for _, args := range [][]string{{}, {"--help"}, {"inspect", "ai-token-budget"}} {
		if code := runExtensions(args, &out, &diagnostics); code != 0 {
			t.Fatalf("%v=%d %s", args, code, &diagnostics)
		}
	}
	for _, args := range [][]string{{"list", "extra"}, {"inspect"}, {"inspect", "unknown"}, {"analyze"}, {"analyze", "ai-token-budget"}, {"analyze", "ai-token-budget", "--input", "missing"}, {"bundle"}, {"unknown"}} {
		if code := runExtensions(args, &out, &diagnostics); code == 0 {
			t.Fatalf("accepted %v", args)
		}
	}
}

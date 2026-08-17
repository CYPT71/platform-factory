package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/CYPT71/platform-factory/internal/plugin"
)

// kubevirtPluginBinary builds the real plugins/kubevirt/cmd/platform-factory-kubevirt
// module once per test run, offline - the same technique
// exitcriteria_test.go's zigPluginBinary uses for the third-party plugin
// exit criterion, applied here to prove the declared->discovered->
// negotiated->verified->available lifecycle actually works for kubevirt,
// not just that dispatchKubeVirt's own routing logic is correct against a
// stub (see TestDispatchKubeVirt* in main_test.go for that half).
var kubevirtPluginBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "platform-factory-kubevirt-plugin-*")
	if err != nil {
		return "", err
	}
	binary := filepath.Join(dir, "platform-factory-kubevirt")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = filepath.Join("..", "..", "plugins", "kubevirt", "cmd", "platform-factory-kubevirt")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build kubevirt plugin: %w: %s", err, output)
	}
	return binary, nil
})

// installSignedKubeVirtPlugin writes a signed plugin directory (mirroring
// plugins/kubevirt/plugin.json's corrected family/capabilities/permissions,
// but with a real digest for the binary this test run actually built, not
// the checked-in template's placeholder all-zero one) and its trusted
// public key, returning the plugin dir and key file.
func installSignedKubeVirtPlugin(t *testing.T) (pluginDir, keyFile string) {
	t.Helper()
	binary, err := kubevirtPluginBinary()
	if err != nil {
		t.Fatalf("build kubevirt plugin: %v", err)
	}
	payload, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "kubevirt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "platform-factory-kubevirt"), payload, 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	manifest := plugin.Manifest{
		APIVersion: plugin.ManifestAPIVersion, Name: "kubevirt", Version: "1.0.0",
		Family: plugin.PluginFamilyRuntime,
		Capabilities: []string{
			"runtime.create", "runtime.start", "runtime.stop", "runtime.restart",
			"runtime.status", "runtime.logs", "runtime.delete", "runtime.rbac",
		},
		Permissions: plugin.PluginPermissions{Network: []string{"kubernetes-api"}, Secrets: []string{"kubeconfig"}},
		Executable:  "platform-factory-kubevirt",
		Digest:      "sha256:" + hex.EncodeToString(digest[:]),
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Sign(private, "test-key"); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, plugin.ManifestFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	keyFile = filepath.Join(root, "key.pem")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(dir), keyFile
}

// TestRunMicroVMKubeVirtCreateThroughRealPlugin proves the full plugin
// lifecycle end to end for --backend=kubevirt: platform-factory microvm
// discovers the plugin directory, verifies its signature and digest,
// starts it as a real (sandboxed where the platform supports it, else
// explicitly degraded via --allow-unsandboxed-plugin) subprocess, performs
// the v1.hello handshake, and dispatches "create" by capability
// (runtime.create) rather than by a hardcoded binary name. No live cluster
// is needed: without --apply the plugin only renders the manifest, so no
// kubectl/virtctl subprocess is invoked from inside it.
func TestRunMicroVMKubeVirtCreateThroughRealPlugin(t *testing.T) {
	freshOperationJournal(t)
	pluginDir, keyFile := installSignedKubeVirtPlugin(t)
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{
		"create", "--backend=kubevirt", "--name=demo", "--namespace=production",
		"--image=registry.example/boot@sha256:" + strings.Repeat("b", 64),
		"--plugin-dir", pluginDir, "--plugin-key", keyFile, "--allow-unsandboxed-plugin",
	}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"kind": "VirtualMachine"`) {
		t.Fatalf("stdout does not contain a rendered VirtualMachine manifest: %s", stdout.String())
	}
}

// TestRunMicroVMKubeVirtRefusedWithoutTrustedKey confirms the deny-by-
// default posture holds for the real kubevirt plugin exactly as it already
// does for a third-party one (TestExitCriterionPluginRefusedWithoutKey):
// a correctly signed manifest is still refused without the matching public
// key, so --backend=kubevirt cannot be satisfied by an untrusted plugin
// directory an attacker placed on disk.
func TestRunMicroVMKubeVirtRefusedWithoutTrustedKey(t *testing.T) {
	pluginDir, _ := installSignedKubeVirtPlugin(t)
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{
		"create", "--backend=kubevirt", "--name=demo", "--namespace=production",
		"--image=registry.example/boot@sha256:" + strings.Repeat("b", 64),
		"--plugin-dir", pluginDir, "--allow-unsandboxed-plugin",
	}, &stdout, &stderr, nil)
	if code == 0 {
		t.Fatal("signed kubevirt plugin accepted without a trusted key")
	}
}

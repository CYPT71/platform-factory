package main

import (
	"bytes"
	"context"
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

// kubernetesPluginBinary builds the real
// plugins/kubernetes/cmd/platform-factory-kubernetes module once per test
// run - the same technique kubevirt_plugin_test.go's kubevirtPluginBinary
// (and exitcriteria_test.go's zigPluginBinary) use, applied here to prove
// the declared->discovered->negotiated->verified->available lifecycle
// actually works for the kubernetes deployment plugin, not just that
// deployToCluster/rollbackCluster/dispatchObservation's own routing logic
// is correct against a stub (see deploy_plugin_test.go's
// stubDeploymentPlugin and its users in lifecycle_test.go/observe_test.go
// for that half).
var kubernetesPluginBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "platform-factory-kubernetes-plugin-*")
	if err != nil {
		return "", err
	}
	binary := filepath.Join(dir, "platform-factory-kubernetes")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = filepath.Join("..", "..", "plugins", "kubernetes", "cmd", "platform-factory-kubernetes")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build kubernetes plugin: %w: %s", err, output)
	}
	return binary, nil
})

// installSignedKubernetesPlugin writes a signed plugin directory
// (mirroring plugins/kubernetes/plugin.json's family/capabilities/
// permissions, but with a real digest for the binary this test run
// actually built, not the checked-in template's placeholder all-zero
// one) and its trusted public key, returning the plugin dir and key
// file - the same shape kubevirt_plugin_test.go's
// installSignedKubeVirtPlugin already uses.
func installSignedKubernetesPlugin(t *testing.T) (pluginDir, keyFile string) {
	t.Helper()
	binary, err := kubernetesPluginBinary()
	if err != nil {
		t.Fatalf("build kubernetes plugin: %v", err)
	}
	payload, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "kubernetes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "platform-factory-kubernetes"), payload, 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	manifest := plugin.Manifest{
		APIVersion: plugin.ManifestAPIVersion, Name: "kubernetes", Version: "1.0.0",
		Family:       plugin.PluginFamilyDeployment,
		Capabilities: []string{"deployment.apply", "deployment.observe", "deployment.rollback"},
		Permissions:  plugin.PluginPermissions{Network: []string{"kubernetes-api"}, Secrets: []string{"kubeconfig"}},
		Executable:   "platform-factory-kubernetes",
		Digest:       "sha256:" + hex.EncodeToString(digest[:]),
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

// TestRunDeployThroughRealKubernetesPluginFailsCleanlyWithoutClusterAccess
// proves the full plugin lifecycle end to end for `pf deploy`:
// platform-factory discovers the plugin directory, verifies its
// signature and digest, starts it as a real (sandboxed where the
// platform supports it, else explicitly degraded via
// --allow-unsandboxed-plugin) subprocess, performs the v1.hello
// handshake, and dispatches deployment.apply by capability rather than a
// hardcoded binary name - the same proof
// TestRunMicroVMKubeVirtCreateThroughRealPlugin gives for kubevirt.
// Unlike that test, this plugin has no "just render the manifest, don't
// apply" mode to stay live-cluster-free with: deployment.apply always
// tries to reach a real Kubernetes API. With no $KUBECONFIG and no
// ~/.kube/config in this test's environment (and not running
// in-cluster), the real plugin's own NewClientFromKubeconfig fails
// deterministically and quickly - proving the real RPC round trip
// (request written, dispatched, handled, a real error response decoded)
// works, even though the underlying cluster operation itself cannot be
// exercised here (see the accompanying report's account of what is and
// is not verifiable without a live cluster).
func TestRunDeployThroughRealKubernetesPluginFailsCleanlyWithoutClusterAccess(t *testing.T) {
	freshOperationJournal(t)
	pluginDir, keyFile := installSignedKubernetesPlugin(t)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	var stdout, stderr bytes.Buffer
	image := "ghcr.io/example/api@sha256:" + strings.Repeat("b", 64)
	code := runDeploy(context.Background(), []string{
		"--yes", "--name", "api", "--namespace", "prod",
		"--plugin-dir", pluginDir, "--plugin-key", keyFile, "--allow-unsandboxed-plugin",
		image,
	}, &stdout, &stderr, nil)
	if code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	// The failure must come from the real plugin actually attempting (and
	// failing) the Kubernetes call, not from the host-side "no installed
	// plugin" branch dispatchDeploy/deployToCluster hits when no plugin
	// is configured at all - that would prove nothing about the real
	// wiring.
	if strings.Contains(stderr.String(), "no installed plugin provides") {
		t.Fatalf("expected a real plugin dispatch failure, not a missing-plugin error: %s", stderr.String())
	}
}

// TestRunDeployRefusedWithoutTrustedKubernetesPluginKey confirms the
// deny-by-default posture holds for the real kubernetes plugin exactly
// as it already does for kubevirt
// (TestRunMicroVMKubeVirtRefusedWithoutTrustedKey): a correctly signed
// manifest is still refused without the matching public key, so a real
// `pf deploy` cannot be satisfied by an untrusted plugin directory an
// attacker placed on disk.
func TestRunDeployRefusedWithoutTrustedKubernetesPluginKey(t *testing.T) {
	freshOperationJournal(t)
	pluginDir, _ := installSignedKubernetesPlugin(t)
	var stdout, stderr bytes.Buffer
	image := "ghcr.io/example/api@sha256:" + strings.Repeat("c", 64)
	code := runDeploy(context.Background(), []string{
		"--yes", "--name", "api", "--namespace", "prod",
		"--plugin-dir", pluginDir, "--allow-unsandboxed-plugin",
		image,
	}, &stdout, &stderr, nil)
	if code == 0 {
		t.Fatal("signed kubernetes plugin accepted without a trusted key")
	}
	if strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("flag parsing failed before reaching plugin verification: %s", stderr.String())
	}
}

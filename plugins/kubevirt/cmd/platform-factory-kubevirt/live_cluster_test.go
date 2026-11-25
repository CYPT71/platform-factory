package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDegradedConditionsAgainstARealClusterRBACAPIOutageAndInterruption runs
// this plugin's real execCommand/runCommand (not the swapped fake used by
// degraded_test.go) against a real Kubernetes API server with the real
// KubeVirt CustomResourceDefinition installed, proving that
// classifyClusterError's categories hold against genuine kubectl output,
// not hand-written stderr fixtures.
//
// Requires:
//   - a reachable cluster via the current kubeconfig context (kind +
//     KubeVirt's operator, e.g. with spec.configuration.developerConfiguration.
//     useEmulation: true so no nested virtualization is needed - the test
//     never waits for a VM to actually boot, only for the API server's own
//     admission/authorization/connectivity behavior)
//   - kubectl on PATH
//
// set PLATFORM_FACTORY_TEST_LIVE_KUBEVIRT=1 to run it.
func TestDegradedConditionsAgainstARealClusterRBACAPIOutageAndInterruption(t *testing.T) {
	if os.Getenv("PLATFORM_FACTORY_TEST_LIVE_KUBEVIRT") != "1" {
		t.Skip("set PLATFORM_FACTORY_TEST_LIVE_KUBEVIRT=1 against a real cluster with the KubeVirt CRD installed to run this live test")
	}
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("kubectl not on PATH")
	}
	if out, err := exec.Command("kubectl", "get", "crd", "virtualmachines.kubevirt.io").CombinedOutput(); err != nil {
		t.Skipf("KubeVirt CRD not installed on the current context: %v\n%s", err, out)
	}

	namespace := "pf-live-degraded-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	runKubectl(t, "create", "namespace", namespace)
	t.Cleanup(func() { runKubectl(t, "delete", "namespace", namespace, "--ignore-not-found", "--wait=false") })

	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })
	execCommand = runCommand

	raw, err := json.Marshal(specParams{
		Name:      "live-degraded-vm",
		Namespace: namespace,
		Image:     "example.invalid/boot@sha256:" + strings.Repeat("a", 64),
		Arch:      "amd64",
		Apply:     true,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("rbac_forbidden", func(t *testing.T) {
		// A ServiceAccount with no KubeVirt permissions at all (only the
		// implicit defaults) must be denied by the real API server's own
		// authorization layer - not a hand-written "Forbidden" string.
		runKubectl(t, "create", "serviceaccount", "no-permissions", "-n", namespace)
		token := runKubectl(t, "create", "token", "no-permissions", "-n", namespace, "--duration=10m")
		kubeconfig := restrictedKubeconfig(t, strings.TrimSpace(token))

		restore := os.Getenv("KUBECONFIG")
		os.Setenv("KUBECONFIG", kubeconfig)
		defer os.Setenv("KUBECONFIG", restore)

		_, err := handleCreate(context.Background(), raw)
		if err == nil || !strings.Contains(err.Error(), "rbac_forbidden") {
			t.Fatalf("expected rbac_forbidden from a real unauthorized ServiceAccount, got %v", err)
		}
	})

	t.Run("api_unavailable", func(t *testing.T) {
		// A kubeconfig pointed at a port nothing is listening on reproduces
		// a real, unreachable API server - not a simulated "connection
		// refused" string.
		kubeconfig := unreachableKubeconfig(t)
		restore := os.Getenv("KUBECONFIG")
		os.Setenv("KUBECONFIG", kubeconfig)
		defer os.Setenv("KUBECONFIG", restore)

		_, err := handleCreate(context.Background(), raw)
		if err == nil || !strings.Contains(err.Error(), "api_unavailable") {
			t.Fatalf("expected api_unavailable against an unreachable API server, got %v", err)
		}
	})

	t.Run("interrupted", func(t *testing.T) {
		// A context canceled before the real kubectl process starts must
		// surface as "interrupted" through runCommand's real exec path,
		// exercised against a real (reachable, authorized) cluster rather
		// than a mock.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := execCommand(ctx, "kubectl", []string{"get", "namespace", namespace}, nil)
		if err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("expected interrupted on a pre-canceled context, got %v", err)
		}
	})
}

func runKubectl(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("kubectl", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// restrictedKubeconfig clones the current context's cluster connection info
// but swaps in token as the only credential, so the resulting kubeconfig
// authenticates as whatever ServiceAccount issued that token.
func restrictedKubeconfig(t *testing.T, token string) string {
	t.Helper()
	server := runKubectl(t, "config", "view", "--minify", "--raw", "-o", "jsonpath={.clusters[0].cluster.server}")
	caData := runKubectl(t, "config", "view", "--minify", "--raw", "-o", "jsonpath={.clusters[0].cluster.certificate-authority-data}")
	path := t.TempDir() + "/restricted.kubeconfig"
	contents := "apiVersion: v1\nkind: Config\nclusters:\n- name: target\n  cluster:\n    server: " + server +
		"\n    certificate-authority-data: " + caData + "\ncontexts:\n- name: target\n  context:\n    cluster: target\n    user: target\ncurrent-context: target\nusers:\n- name: target\n  user:\n    token: " + token + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// unreachableKubeconfig points at a real cluster's CA/credentials but a
// server address nothing listens on.
func unreachableKubeconfig(t *testing.T) string {
	t.Helper()
	caData := runKubectl(t, "config", "view", "--minify", "--raw", "-o", "jsonpath={.clusters[0].cluster.certificate-authority-data}")
	path := t.TempDir() + "/unreachable.kubeconfig"
	contents := "apiVersion: v1\nkind: Config\nclusters:\n- name: target\n  cluster:\n    server: https://127.0.0.1:1\n    certificate-authority-data: " + caData +
		"\ncontexts:\n- name: target\n  context:\n    cluster: target\n    user: target\ncurrent-context: target\nusers:\n- name: target\n  user:\n    token: unreachable-placeholder\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

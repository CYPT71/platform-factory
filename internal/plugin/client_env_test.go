package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// envProbePluginPath builds testdata/plugins/sandboxprobe - the same
// separate-module plugin sandbox_linux_test.go uses for its Linux-only
// namespace assertions - so this cross-platform test reuses the identical
// binary instead of introducing a second fixture. It cannot reuse
// sandbox_linux_test.go's own sandboxProbePluginPath var directly: that
// file carries a linux build tag, so the var does not exist on other
// platforms, and this test must run everywhere (it is, in fact, most
// relevant off Linux - see sandbox_other.go's wrapWithPluginSandbox, which
// always fails there, so every plugin start on macOS/Windows already takes
// the unsandboxed path this test exercises).
var envProbePluginPath = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "platform-factory-envprobe-*")
	if err != nil {
		return "", err
	}
	binary := filepath.Join(dir, "sandboxprobe")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = filepath.Join("..", "..", "testdata", "plugins", "sandboxprobe")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build env probe plugin: %w: %s", err, output)
	}
	return binary, nil
})

// observedPluginEnv starts binary unsandboxed (forcing wrapWithPluginSandbox
// to fail, the same stub sandbox_linux_test.go's
// TestClientRefusesWhenSandboxUnavailableUnlessPolicyAllowsDegradation uses
// to exercise the exact fallback branch a real host with no CAP_SYS_ADMIN
// hits - the field report's actual failure mode) with family/permissions,
// calls its env-probe capability, and returns the child's reported
// environment as a name->value map.
func observedPluginEnv(t *testing.T, binary string, family PluginFamily, permissions PluginPermissions) map[string]string {
	t.Helper()
	original := pluginSandboxWrapper
	pluginSandboxWrapper = func(*exec.Cmd, PluginFamily, PluginPermissions) error {
		return errors.New("sandbox unavailable (stub, simulating no CAP_SYS_ADMIN)")
	}
	defer func() { pluginSandboxWrapper = original }()

	client, err := StartAllowingUnsandboxedWithManifest(context.Background(), binary, nil, nil, family, permissions)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer client.Close()

	var result map[string][]string
	if err := client.Call(context.Background(), "v1.observe.env-probe", nil, &result); err != nil {
		t.Fatalf("call: %v", err)
	}
	observed := map[string]string{}
	for _, entry := range result["env"] {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		observed[name] = value
	}
	return observed
}

// TestUnsandboxedPluginDeclaringKubeconfigReceivesFilteredHostEnvironment is
// the direct regression test for the field report: a manifest-driven plugin
// (like plugins/kubernetes, which declares Permissions.Secrets:
// ["kubeconfig"]) started via the unsandboxed fallback used to receive
// cmd.Env == []string{"PATH=/usr/bin:/bin"} and nothing else, because
// newPluginCommand hardcoded that fallback instead of computing an
// environment from the real host environment and the plugin's declared
// permissions (see client.go's newPluginCommand and its doc comment). This
// proves KUBECONFIG and HOME now reach the plugin, while an unrelated
// secret-looking host variable does not - filterPluginEnvironment's
// allow-list is doing the real work, on a real, non-empty input.
func TestUnsandboxedPluginDeclaringKubeconfigReceivesFilteredHostEnvironment(t *testing.T) {
	binary, err := envProbePluginPath()
	if err != nil {
		t.Fatalf("build env probe plugin: %v", err)
	}

	t.Setenv("KUBECONFIG", "/host/kubeconfig-for-test")
	t.Setenv("HOME", "/host/home-for-test")
	t.Setenv("SECRET_VAR", "must-not-leak")

	observed := observedPluginEnv(t, binary, PluginFamilyDeployment, PluginPermissions{Secrets: []string{"kubeconfig"}})

	if observed["KUBECONFIG"] != "/host/kubeconfig-for-test" {
		t.Fatalf("KUBECONFIG=%q, want %q (observed=%+v)", observed["KUBECONFIG"], "/host/kubeconfig-for-test", observed)
	}
	if observed["HOME"] != "/host/home-for-test" {
		t.Fatalf("HOME=%q, want %q (observed=%+v)", observed["HOME"], "/host/home-for-test", observed)
	}
	if _, leaked := observed["SECRET_VAR"]; leaked {
		t.Fatalf("SECRET_VAR leaked to the plugin: %+v", observed)
	}
	if observed["PATH"] == "" {
		t.Fatalf("PATH missing from plugin environment: %+v", observed)
	}
	t.Logf("plugin observed environment: %+v", observed)
}

// TestUnsandboxedPluginWithoutKubeconfigPermissionDoesNotReceiveItEvenWhenHostHasIt
// is the negative case the fix must not silently over-grant: a plugin whose
// manifest does NOT declare the kubeconfig secret must not receive
// KUBECONFIG/HOME even though they are present in the host's own
// environment and even though the plugin does hold some other, unrelated
// permission (network) - guards against a fix that accidentally passes the
// whole host environment through instead of the permission-gated subset.
func TestUnsandboxedPluginWithoutKubeconfigPermissionDoesNotReceiveItEvenWhenHostHasIt(t *testing.T) {
	binary, err := envProbePluginPath()
	if err != nil {
		t.Fatalf("build env probe plugin: %v", err)
	}

	t.Setenv("KUBECONFIG", "/host/kubeconfig-for-test")
	t.Setenv("HOME", "/host/home-for-test")

	observed := observedPluginEnv(t, binary, PluginFamilyDeployment, PluginPermissions{Network: []string{"kubernetes-api"}})

	if _, leaked := observed["KUBECONFIG"]; leaked {
		t.Fatalf("KUBECONFIG leaked to a plugin that did not declare the kubeconfig secret: %+v", observed)
	}
	if _, leaked := observed["HOME"]; leaked {
		t.Fatalf("HOME leaked to a plugin that did not declare the kubeconfig secret: %+v", observed)
	}
	if observed["PATH"] == "" {
		t.Fatalf("PATH missing from plugin environment: %+v", observed)
	}
}

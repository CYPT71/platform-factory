//go:build linux

package plugin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// sandboxProbePluginPath builds testdata/sandboxprobe — a separate Go
// module that imports only the public sdk/plugin SDK — proving these
// sandbox properties hold for a genuinely external plugin binary, not just
// one built from inside the module.
var sandboxProbePluginPath = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "platform-factory-sandboxprobe-*")
	if err != nil {
		return "", err
	}
	binary := filepath.Join(dir, "sandboxprobe")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = filepath.Join("..", "..", "testdata", "plugins", "sandboxprobe")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build sandbox probe plugin: %w: %s", err, output)
	}
	return binary, nil
})

// TestStartDeniesOutboundNetworkToPlugin proves, from inside a genuinely
// separate plugin subprocess, that Start's namespace sandbox actually
// applies: with no interface at all in the fresh network namespace (not
// even loopback, since no plugin capability needs it - see
// wrapWithPluginSandbox), an outbound connection attempt fails immediately
// rather than reaching the network or timing out.
func TestStartDeniesOutboundNetworkToPlugin(t *testing.T) {
	binary, err := sandboxProbePluginPath()
	if err != nil {
		t.Fatalf("build sandbox probe plugin: %v", err)
	}
	client, err := Start(context.Background(), binary, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer client.Close()

	var result map[string]string
	if err := client.Call(context.Background(), "v1.observe.net-probe", nil, &result); err != nil {
		t.Fatalf("call: %v", err)
	}
	if result["result"] != "denied" {
		t.Fatalf("net-probe result = %+v, want network access denied", result)
	}
	t.Logf("plugin observed: %s", result["detail"])
}

// TestStartSetsNoNewPrivilegesOnPlugin proves the plugin subprocess itself
// cannot regain privileges through a setuid/setgid or file-capability
// executable, mirroring internal/executor's equivalent stage guarantee.
func TestStartSetsNoNewPrivilegesOnPlugin(t *testing.T) {
	binary, err := sandboxProbePluginPath()
	if err != nil {
		t.Fatalf("build sandbox probe plugin: %v", err)
	}
	client, err := Start(context.Background(), binary, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer client.Close()

	var result map[string]string
	if err := client.Call(context.Background(), "v1.observe.priv-probe", nil, &result); err != nil {
		t.Fatalf("call: %v", err)
	}
	if result["no_new_privs"] != "1" {
		t.Fatalf("no_new_privs = %+v, want \"1\"", result)
	}
}

func TestStartBoundsKernelResources(t *testing.T) {
	binary, err := sandboxProbePluginPath()
	if err != nil {
		t.Fatalf("build sandbox probe plugin: %v", err)
	}
	client, err := Start(context.Background(), binary, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer client.Close()

	var result map[string]string
	if err := client.Call(context.Background(), "v1.observe.isolation-probe", nil, &result); err != nil {
		t.Fatalf("call: %v", err)
	}
	want := map[string]string{
		"core": "0", "file_size": "16777216",
		"open_files": "256", "cpu": "60",
	}
	for name, value := range want {
		if result[name] != value {
			t.Fatalf("%s=%q, want %q (all=%+v)", name, result[name], value, result)
		}
	}
}

func TestWrapWithPluginSandboxRefusesUnprovenProjectRootIsolation(t *testing.T) {
	root := t.TempDir()
	restore := SetPluginSandboxConfigFunc(func(*exec.Cmd) PluginSandboxOptions {
		return WithProjectRoot(root)
	})
	defer restore()

	cmd := exec.Command("/bin/true")
	originalPath := cmd.Path
	originalArgs := append([]string(nil), cmd.Args...)
	if err := wrapWithPluginSandbox(cmd); err == nil {
		t.Fatal("wrapWithPluginSandbox accepted an unproven project-root isolation requirement")
	}
	if cmd.Path != originalPath {
		t.Fatalf("failed wrapper mutated command path: got %q, want %q", cmd.Path, originalPath)
	}
	if fmt.Sprint(cmd.Args) != fmt.Sprint(originalArgs) {
		t.Fatalf("failed wrapper mutated command args: got %v, want %v", cmd.Args, originalArgs)
	}
	if cmd.SysProcAttr != nil {
		t.Fatal("failed wrapper partially configured process sandbox")
	}
}

func TestProjectRootRequirementFailsClosedUnlessPolicyAllowsDegradation(t *testing.T) {
	binary, err := sandboxProbePluginPath()
	if err != nil {
		t.Fatalf("build sandbox probe plugin: %v", err)
	}
	restore := SetPluginSandboxConfigFunc(func(*exec.Cmd) PluginSandboxOptions {
		return WithProjectRoot(t.TempDir())
	})
	defer restore()

	client, err := Start(context.Background(), binary, nil, nil)
	if err == nil {
		client.Close()
		t.Fatal("Start accepted a project-root requirement it could not apply")
	}

	client, err = StartAllowingUnsandboxed(context.Background(), binary, nil, nil)
	if err != nil {
		t.Fatalf("explicit degradation policy did not permit launch: %v", err)
	}
	defer client.Close()
}

func TestFilterPluginEnvironment(t *testing.T) {
	// Test that sensitive environment variables are filtered out
	env := []string{
		"PATH=/usr/bin:/bin",
		"PLATFORM_FACTORY_TRACE_ID=test123",
		"HOME=/home/user",
		"SECRET_PASSWORD=dontleak",
		"LANG=en_US.UTF-8",
		"LC_ALL=C",
		"SSH_AUTH_SOCK=/tmp/ssh",
		"AWS_SECRET_ACCESS_KEY=secret",
	}

	filtered := filterPluginEnvironment(env)

	// Check that PATH is kept
	found := false
	for _, e := range filtered {
		if strings.HasPrefix(e, "PATH=") {
			found = true
			if e != "PATH=/usr/bin:/bin" {
				t.Errorf("expected filtered PATH to be /usr/bin:/bin, got %q", e)
			}
			break
		}
	}
	if !found {
		t.Error("expected PATH to be in filtered environment")
	}

	// Check that PLATFORM_FACTORY_* is kept
	found = false
	for _, e := range filtered {
		if strings.HasPrefix(e, "PLATFORM_FACTORY_") {
			found = true
			if e != "PLATFORM_FACTORY_TRACE_ID=test123" {
				t.Errorf("expected PLATFORM_FACTORY_TRACE_ID, got %q", e)
			}
			break
		}
	}
	if !found {
		t.Error("expected PLATFORM_FACTORY_TRACE_ID to be in filtered environment")
	}

	// Check that sensitive variables are removed
	sensitive := []string{"HOME=", "SECRET_", "SSH_AUTH_SOCK=", "AWS_SECRET"}
	for _, prefix := range sensitive {
		for _, e := range filtered {
			if strings.HasPrefix(e, prefix) {
				t.Errorf("sensitive variable %q should be filtered out, but found in: %v", prefix, filtered)
			}
		}
	}

	// Check that LANG/LC variables are kept
	found = false
	for _, e := range filtered {
		if strings.HasPrefix(e, "LANG=") || strings.HasPrefix(e, "LC_") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected LANG or LC_ variables to be in filtered environment")
	}
}

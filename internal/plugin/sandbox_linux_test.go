//go:build linux

package plugin

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
		"open_files": "256", "cpu": "60", "processes": "64",
		// Start (no manifest, no family) resolves defaultPermissionProfile,
		// whose zero-value MemoryMiB means "apply no RLIMIT_AS at all" -
		// see TestStartWithFamilyAppliesPermissionProfile below for the
		// contrasting case where a declared family does bound it.
		"address_space": strconv.FormatUint(math.MaxUint64, 10),
	}
	for name, value := range want {
		if result[name] != value {
			t.Fatalf("%s=%q, want %q (all=%+v)", name, result[name], value, result)
		}
	}
}

// TestStartWithFamilyAppliesPermissionProfile proves permission_profile.go's
// per-family resource ceilings actually reach the sandboxed process, and
// that two different families genuinely produce two different ceilings
// (not, say, both silently falling back to the same default): PluginFamilyLanguage
// and PluginFamilyBuild declare different MemoryMiB in permissionProfiles,
// so their RLIMIT_AS must differ by exactly that ratio.
func TestStartWithFamilyAppliesPermissionProfile(t *testing.T) {
	binary, err := sandboxProbePluginPath()
	if err != nil {
		t.Fatalf("build sandbox probe plugin: %v", err)
	}

	observedAddressSpace := func(family PluginFamily) uint64 {
		t.Helper()
		client, err := StartWithFamily(context.Background(), binary, nil, nil, family)
		if err != nil {
			t.Fatalf("start(%s): %v", family, err)
		}
		defer client.Close()
		var result map[string]string
		if err := client.Call(context.Background(), "v1.observe.isolation-probe", nil, &result); err != nil {
			t.Fatalf("call(%s): %v", family, err)
		}
		value, err := strconv.ParseUint(result["address_space"], 10, 64)
		if err != nil {
			t.Fatalf("parse address_space for %s: %v (all=%+v)", family, err, result)
		}
		return value
	}

	languageLimit := observedAddressSpace(PluginFamilyLanguage)
	buildLimit := observedAddressSpace(PluginFamilyBuild)

	wantLanguage := permissionProfiles[PluginFamilyLanguage].MemoryMiB << 20
	wantBuild := permissionProfiles[PluginFamilyBuild].MemoryMiB << 20
	if languageLimit != wantLanguage {
		t.Fatalf("language family address_space=%d, want %d", languageLimit, wantLanguage)
	}
	if buildLimit != wantBuild {
		t.Fatalf("build family address_space=%d, want %d", buildLimit, wantBuild)
	}
	if languageLimit == buildLimit {
		t.Fatalf("language and build families produced the same RLIMIT_AS (%d); permission_profile.go is not actually varying by family", languageLimit)
	}
}

// TestStartIsolatesPluginTempDirectory proves, from inside a genuinely
// separate plugin subprocess, two properties of Start's temp-directory
// isolation at once (see internal/plugin/sandbox_linux.go's
// isolateTempDirectory for why both, not just the first, matter):
//  1. the plugin's own $TMPDIR is a private, empty scratch tmpfs, not the
//     shared /tmp - proving isolation is active at all;
//  2. a file created directly under the real, shared /tmp is still
//     reachable from inside the plugin at that same path - proving
//     isolation did not regress detect/freeze/plan's ability to read a
//     real, caller-chosen project path that happens to live under /tmp,
//     the exact regression an earlier version of this change caused
//     (TestThirdPartyPluginAddsLanguageWithoutRecompilingTheHost, which
//     passes a t.TempDir() project root over RPC, started failing).
func TestStartIsolatesPluginTempDirectory(t *testing.T) {
	binary, err := sandboxProbePluginPath()
	if err != nil {
		t.Fatalf("build sandbox probe plugin: %v", err)
	}
	canary, err := os.CreateTemp("/tmp", "platform-factory-sandbox-canary-*")
	if err != nil {
		t.Fatalf("create host canary file: %v", err)
	}
	canaryPath := canary.Name()
	if err := canary.Close(); err != nil {
		t.Fatalf("close host canary file: %v", err)
	}
	defer os.Remove(canaryPath)

	client, err := Start(context.Background(), binary, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer client.Close()

	var result map[string]string
	if err := client.Call(context.Background(), "v1.observe.tmp-probe",
		map[string]string{"host_canary_path": canaryPath}, &result); err != nil {
		t.Fatalf("call: %v", err)
	}
	if result["tmpdir"] == "" {
		// isolateTempDirectory is documented best-effort: a host that
		// denies "mount namespace private" (observed in CI as "plugin
		// sandbox: make mount namespace private: permission denied" -
		// visible now that internal/plugin/client.go forwards the
		// sandbox helper's stderr instead of discarding it) leaves
		// TMPDIR unset rather than trusting an unisolated value. That is
		// the function's documented, accepted degraded-but-safe outcome,
		// not a defect this test can meaningfully assert against - skip
		// rather than fail, the same way internal/executor's
		// TestSandboxFailsClosedWithoutCgroupSupport skips for
		// unavailable cgroup delegation.
		t.Skip("mount namespace isolation is unavailable on this host; isolateTempDirectory degraded to its documented fallback")
	}
	if result["tmpdir"] == "/tmp" {
		t.Fatalf("plugin TMPDIR is not a private scratch directory: %+v", result)
	}
	if result["scratch_entry_count"] != "0" {
		t.Fatalf("plugin's private scratch directory is not empty: %+v", result)
	}
	if result["shared_tmp_reachable"] != "true" {
		t.Fatalf("a file under the real, shared /tmp is not reachable from inside the plugin: %+v", result)
	}
}

// hostNetnsIdentity reads this test process's own /proc/self/ns/net, the
// same identifier handleNetnsProbe reports from inside a plugin.
func hostNetnsIdentity(t *testing.T) string {
	t.Helper()
	target, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		t.Fatalf("read host netns: %v", err)
	}
	return target
}

// TestStartWithManifestGrantsHostNetworkOnlyWhenPermissionsDeclareIt proves
// hostNetworkGranted's effect on a real plugin subprocess in both
// directions: a plugin with no declared network permission still lands in
// its own fresh, isolated network namespace (identical to Start's existing
// behavior - see TestStartDeniesOutboundNetworkToPlugin), while a plugin
// that declares Permissions.Network under a non-language family actually
// shares the host's network namespace, proven by comparing
// /proc/self/ns/net identifiers directly rather than depending on real
// outbound connectivity being reachable from the test environment.
func TestStartWithManifestGrantsHostNetworkOnlyWhenPermissionsDeclareIt(t *testing.T) {
	binary, err := sandboxProbePluginPath()
	if err != nil {
		t.Fatalf("build sandbox probe plugin: %v", err)
	}
	hostNetns := hostNetnsIdentity(t)

	observedNetns := func(family PluginFamily, permissions PluginPermissions) string {
		t.Helper()
		client, err := StartWithManifest(context.Background(), binary, nil, nil, family, permissions)
		if err != nil {
			t.Fatalf("start(%s, %+v): %v", family, permissions, err)
		}
		defer client.Close()
		var result map[string]string
		if err := client.Call(context.Background(), "v1.observe.netns-probe", nil, &result); err != nil {
			t.Fatalf("call(%s, %+v): %v", family, permissions, err)
		}
		return result["netns"]
	}

	isolated := observedNetns(PluginFamilyRuntime, PluginPermissions{})
	if isolated == hostNetns {
		t.Fatalf("plugin with no declared network permission shares the host netns %q; wrapWithPluginSandbox should have isolated it", hostNetns)
	}

	granted := observedNetns(PluginFamilyRuntime, PluginPermissions{Network: []string{"kubernetes-api"}})
	if granted != hostNetns {
		t.Fatalf("plugin with a declared network permission has netns %q, want the host's %q", granted, hostNetns)
	}

	// Defense in depth: Validate already refuses a language-family manifest
	// that declares network permissions at all, but hostNetworkGranted must
	// not honor one anyway if it somehow got this far unvalidated.
	stillIsolated := observedNetns(PluginFamilyLanguage, PluginPermissions{Network: []string{"kubernetes-api"}})
	if stillIsolated == hostNetns {
		t.Fatalf("language-family plugin shares the host netns %q despite the family; hostNetworkGranted must never grant network to language plugins", hostNetns)
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
	if err := wrapWithPluginSandbox(cmd, "", PluginPermissions{}); err == nil {
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

	filtered := filterPluginEnvironment(env, PluginPermissions{})

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

// TestFilterPluginEnvironmentGrantsKubeconfigOnlyWhenDeclared proves
// KUBECONFIG/HOME pass through only for a plugin whose own Permissions
// declares "kubeconfig" as a secret - not for every plugin, and not merely
// because it happens to also have host network access (a separate,
// independent grant - see hostNetworkGranted).
func TestFilterPluginEnvironmentGrantsKubeconfigOnlyWhenDeclared(t *testing.T) {
	env := []string{"PATH=/usr/bin:/bin", "HOME=/home/user", "KUBECONFIG=/home/user/.kube/config"}

	withoutGrant := filterPluginEnvironment(env, PluginPermissions{Network: []string{"kubernetes-api"}})
	for _, e := range withoutGrant {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "KUBECONFIG=") {
			t.Fatalf("HOME/KUBECONFIG leaked to a plugin that did not declare the kubeconfig secret: %v", withoutGrant)
		}
	}

	withGrant := filterPluginEnvironment(env, PluginPermissions{Secrets: []string{"kubeconfig"}})
	var sawHome, sawKubeconfig bool
	for _, e := range withGrant {
		sawHome = sawHome || e == "HOME=/home/user"
		sawKubeconfig = sawKubeconfig || e == "KUBECONFIG=/home/user/.kube/config"
	}
	if !sawHome || !sawKubeconfig {
		t.Fatalf("HOME/KUBECONFIG not passed through to a plugin that declared the kubeconfig secret: %v", withGrant)
	}
}

//go:build linux

package executor

import (
	"bytes"
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/core"
)

// sandboxTestExecutor returns a sandboxed executor over a fresh stage
// root with the host toolchain bind-mounted read-only, or skips when
// the host cannot create user namespaces.
func sandboxTestExecutor(t *testing.T, root string) (*Executor, SandboxSupport) {
	t.Helper()
	support := ProbeSandbox()
	if !support.UserNamespaces {
		t.Skipf("no user namespace support: %s", support.Details["user-namespaces"])
	}
	sources := map[string]string{}
	for _, dir := range []string{"/bin", "/usr", "/lib", "/lib64", "/etc"} {
		if _, err := os.Stat(dir); err == nil {
			sources[strings.TrimPrefix(dir, "/")] = dir
		}
	}
	executor, err := NewSandboxed(root, nil, support, sources)
	if err != nil {
		t.Fatal(err)
	}
	return executor, support
}

func toolchainMounts(t *testing.T, executor *Executor) []core.Mount {
	t.Helper()
	var mounts []core.Mount
	for id := range executor.mountSources {
		mounts = append(mounts, core.Mount{Source: id, Target: "/" + id, ReadOnly: true})
	}
	return mounts
}

func runSandboxStage(t *testing.T, executor *Executor, stage core.Stage) Result {
	t.Helper()
	_ = executor.Run(context.Background(), stage)
	results := executor.Results()
	if len(results) == 0 {
		t.Fatal("no result recorded")
	}
	return results[len(results)-1]
}

func TestSandboxConfinesFilesystemWritesToStageRoot(t *testing.T) {
	root := t.TempDir()
	executor, _ := sandboxTestExecutor(t, root)
	stage := core.Stage{
		ID:      "write",
		Command: core.Command{Executable: "sh", Args: []string{"-c", "echo confined > /out.txt && cat /out.txt"}},
		Mounts:  toolchainMounts(t, executor),
	}
	result := runSandboxStage(t, executor, stage)
	if result.ExitCode != 0 {
		t.Fatalf("result=%+v", result)
	}
	data, err := os.ReadFile(filepath.Join(root, "out.txt"))
	if err != nil || strings.TrimSpace(string(data)) != "confined" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestSandboxRunsAsPidOneWithPrivateProc(t *testing.T) {
	root := t.TempDir()
	executor, _ := sandboxTestExecutor(t, root)
	stage := core.Stage{
		ID:      "pid",
		Command: core.Command{Executable: "sh", Args: []string{"-c", "echo pid=$$ && ls /proc | grep -c '^[0-9]'"}},
		Mounts:  toolchainMounts(t, executor),
	}
	result := runSandboxStage(t, executor, stage)
	if result.ExitCode != 0 {
		t.Fatalf("result=%+v", result)
	}
	output := string(result.Stdout)
	if !strings.Contains(output, "pid=1") {
		t.Fatalf("stage is not PID 1 in its namespace: %s", output)
	}
}

func TestSandboxNetworkNoneHasOnlyLoopback(t *testing.T) {
	root := t.TempDir()
	executor, _ := sandboxTestExecutor(t, root)
	stage := core.Stage{
		ID:      "net",
		Network: core.NetworkNone,
		Command: core.Command{Executable: "sh", Args: []string{"-c", "cat /proc/net/dev"}},
		Mounts:  toolchainMounts(t, executor),
	}
	result := runSandboxStage(t, executor, stage)
	if result.ExitCode != 0 {
		t.Fatalf("result=%+v", result)
	}
	output := string(result.Stdout)
	if !strings.Contains(output, "lo:") {
		t.Fatalf("loopback missing: %s", output)
	}
	for _, line := range strings.Split(output, "\n")[2:] {
		name, _, found := strings.Cut(strings.TrimSpace(line), ":")
		if found && name != "lo" {
			t.Fatalf("unexpected interface %q in network namespace: %s", name, output)
		}
	}
	if _, ok := executor.support.Details["user-namespaces"]; ok {
		t.Fatalf("unexpected detail: %v", executor.support.Details)
	}
	full := core.Stage{
		ID: "full", Network: core.NetworkFull,
		Command: core.Command{Executable: "sh"}, Mounts: toolchainMounts(t, executor),
	}
	cmd, _, _, _, err := executor.prepareSandboxed(context.Background(), full)
	if err != nil {
		t.Fatalf("network full rejected: %v", err)
	}
	if cmd.SysProcAttr.Cloneflags&syscall.CLONE_NEWNET != 0 {
		t.Fatal("network full unexpectedly creates an isolated network namespace")
	}
}

func TestSandboxReadOnlyRootRejectsWrites(t *testing.T) {
	root := t.TempDir()
	executor, _ := sandboxTestExecutor(t, root)
	stage := core.Stage{
		ID:      "ro",
		Command: core.Command{Executable: "sh", Args: []string{"-c", "echo denied > /out.txt"}},
		Mounts:  toolchainMounts(t, executor),
		Sandbox: core.SandboxPolicy{ReadOnlyRoot: true},
	}
	result := runSandboxStage(t, executor, stage)
	if result.ExitCode == 0 {
		t.Fatalf("write to a read-only root succeeded: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("read-only root produced a file: %v", err)
	}
	writable := core.Stage{
		ID:      "ro-tmp",
		Command: core.Command{Executable: "sh", Args: []string{"-c", "echo scratch > /tmp/scratch && cat /tmp/scratch"}},
		Mounts:  toolchainMounts(t, executor),
		Sandbox: core.SandboxPolicy{ReadOnlyRoot: true},
	}
	if result := runSandboxStage(t, executor, writable); result.ExitCode != 0 {
		t.Fatalf("tmpfs /tmp not writable under read-only root: %+v", result)
	}
}

func TestSandboxReadOnlyMountRejectsWrites(t *testing.T) {
	root := t.TempDir()
	executor, _ := sandboxTestExecutor(t, root)
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "input.txt"), []byte("input"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor.mountSources["data"] = data
	stage := core.Stage{
		ID: "romount",
		Command: core.Command{Executable: "sh", Args: []string{"-c",
			"cat /data/input.txt && ! sh -c 'echo x > /data/deny.txt'"}},
		Mounts: append(toolchainMounts(t, executor), core.Mount{Source: "data", Target: "/data", ReadOnly: true}),
	}
	result := runSandboxStage(t, executor, stage)
	if result.ExitCode != 0 {
		t.Fatalf("result=%+v stderr=%s", result, result.Stderr)
	}
	if _, err := os.Stat(filepath.Join(data, "deny.txt")); !os.IsNotExist(err) {
		t.Fatal("read-only bind mount accepted a write")
	}
}

func TestSandboxNonRootSetsNoNewPrivileges(t *testing.T) {
	root := t.TempDir()
	executor, _ := sandboxTestExecutor(t, root)
	stage := core.Stage{
		ID:      "nonroot",
		Command: core.Command{Executable: "grep", Args: []string{"NoNewPrivs", "/proc/self/status"}},
		Mounts:  toolchainMounts(t, executor),
		Sandbox: core.SandboxPolicy{NonRoot: true},
	}
	result := runSandboxStage(t, executor, stage)
	if result.ExitCode != 0 {
		t.Fatalf("result=%+v stderr=%s", result, result.Stderr)
	}
	if !strings.Contains(string(result.Stdout), "NoNewPrivs:\t1") {
		t.Fatalf("no_new_privs not set: %s", result.Stdout)
	}
}

func TestSandboxSecretsAreDeliveredInMemoryAndRedacted(t *testing.T) {
	root := t.TempDir()
	executor, _ := sandboxTestExecutor(t, root)
	secretValue := "swordfish-0123456789"
	executor.WithSecretResolver(staticResolver{"registry-token": []byte(secretValue)})
	stage := core.Stage{
		ID: "secret",
		Command: core.Command{Executable: "sh", Args: []string{"-c",
			"cat /run/secrets/token && echo && ls -l /run/secrets/token"}},
		Mounts:  toolchainMounts(t, executor),
		Secrets: []core.SecretReference{{ID: "registry-token", Target: "/run/secrets/token"}},
	}
	result := runSandboxStage(t, executor, stage)
	if result.ExitCode != 0 {
		t.Fatalf("result=%+v stderr=%s", result, result.Stderr)
	}
	if bytes.Contains(result.Stdout, []byte(secretValue)) || bytes.Contains(result.Stderr, []byte(secretValue)) {
		t.Fatalf("captured output leaks the secret: %s", result.Stdout)
	}
	if !bytes.Contains(result.Stdout, []byte("[secret]")) {
		t.Fatalf("secret was not delivered: %s", result.Stdout)
	}
	// The secret must not survive anywhere under the stage root: the
	// tmpfs vanished with the mount namespace.
	found := false
	_ = filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return nil
		}
		data, readErr := os.ReadFile(name)
		if readErr == nil && bytes.Contains(data, []byte(secretValue)) {
			found = true
		}
		return nil
	})
	if found {
		t.Fatal("secret value persisted under the stage root")
	}
}

type staticResolver map[string][]byte

func (r staticResolver) Resolve(_ context.Context, id string) ([]byte, error) {
	value, ok := r[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	return value, nil
}

func TestPlainExecutorRefusesSecretsAndSandboxPolicies(t *testing.T) {
	executor := New(t.TempDir(), nil)
	if err := executor.Run(context.Background(), core.Stage{
		ID: "secret", Command: core.Command{Executable: "true"},
		Secrets: []core.SecretReference{{ID: "token", Target: "/run/secret"}},
	}); err == nil || !strings.Contains(err.Error(), "sandboxed executor") {
		t.Fatalf("err=%v", err)
	}
	if err := executor.Run(context.Background(), core.Stage{
		ID: "ro", Command: core.Command{Executable: "true"},
		Sandbox: core.SandboxPolicy{ReadOnlyRoot: true},
	}); err == nil || !strings.Contains(err.Error(), "sandbox policies") {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveNetworkRequiresPinnedForwarder(t *testing.T) {
	executor := New(t.TempDir(), nil)
	executor.sandboxed = true
	executor.support = SandboxSupport{UserNamespaces: true, Details: map[string]string{}}
	stage := core.Stage{
		ID:      "resolve",
		Command: core.Command{Executable: "true"},
		Network: core.NetworkResolve,
	}
	if _, _, _, _, err := executor.prepareSandboxed(context.Background(), stage); err == nil ||
		!strings.Contains(err.Error(), "configure an explicit") {
		t.Fatalf("missing forwarder err=%v", err)
	}
	executor.WithDNSForwarder(&testNetworkRelay{})
	if _, _, _, _, err := executor.prepareSandboxed(context.Background(), stage); err == nil ||
		!strings.Contains(err.Error(), "explicit upstream") {
		t.Fatalf("unpinned forwarder err=%v", err)
	}
}

func TestResolveNetworkBuildsInheritedRelay(t *testing.T) {
	executor := New(t.TempDir(), nil)
	executor.sandboxed = true
	executor.support = SandboxSupport{UserNamespaces: true, Details: map[string]string{}}
	executor.WithDNSForwarder(&testNetworkRelay{upstream: netip.MustParseAddrPort("127.0.0.1:5353")})
	ctx, cancel := context.WithCancel(context.Background())
	cmd, _, relay, _, err := executor.prepareSandboxed(ctx, core.Stage{
		ID:      "resolve",
		Command: core.Command{Executable: "true"},
		Network: core.NetworkResolve,
	})
	if err != nil {
		t.Fatal(err)
	}
	if relay == nil || len(cmd.ExtraFiles) != 1 {
		t.Fatalf("relay=%v extra files=%d", relay, len(cmd.ExtraFiles))
	}
	cancel()
	_ = relay.Close()
	for _, file := range cmd.ExtraFiles {
		_ = file.Close()
	}
}

func TestSandboxFailsClosedWithoutCgroupSupport(t *testing.T) {
	root := t.TempDir()
	executor, support := sandboxTestExecutor(t, root)
	if support.CgroupPIDs {
		t.Skip("cgroup pids delegation available; the fail-closed path needs its absence")
	}
	err := executor.Run(context.Background(), core.Stage{
		ID: "pids", Command: core.Command{Executable: "true"},
		Resources: core.ResourceLimits{PIDs: 16},
	})
	if err == nil || !strings.Contains(err.Error(), "process-count limit") {
		t.Fatalf("err=%v", err)
	}
}

func TestSandboxCgroupEnforcesPidLimit(t *testing.T) {
	root := t.TempDir()
	executor, support := sandboxTestExecutor(t, root)
	if !support.CgroupPIDs {
		t.Skipf("no cgroup pids delegation: %v", support.Details)
	}
	stage := core.Stage{
		ID: "forker",
		Command: core.Command{Executable: "sh", Args: []string{"-c",
			"set -e; for i in $(seq 1 64); do sleep 5 & done; wait"}},
		Mounts:    toolchainMounts(t, executor),
		Resources: core.ResourceLimits{PIDs: 8},
	}
	result := runSandboxStage(t, executor, stage)
	if result.ExitCode == 0 {
		t.Fatalf("fork storm survived pids.max: %+v", result)
	}
}

func TestSandboxCgroupAppliesCPUQuota(t *testing.T) {
	root := t.TempDir()
	executor, support := sandboxTestExecutor(t, root)
	if !support.CgroupCPU {
		t.Skipf("no cgroup cpu delegation: %v", support.Details)
	}
	stage := core.Stage{
		ID:        "cpu",
		Command:   core.Command{Executable: "sh", Args: []string{"-c", "cat /sys/fs/cgroup/cpu.max 2>/dev/null || true"}},
		Mounts:    toolchainMounts(t, executor),
		Resources: core.ResourceLimits{CPUMilli: 500},
	}
	result := runSandboxStage(t, executor, stage)
	if result.ExitCode != 0 {
		t.Fatalf("result=%+v stderr=%s", result, result.Stderr)
	}
}

func TestProbeSandboxReportsDetails(t *testing.T) {
	support := ProbeSandbox()
	if !support.UserNamespaces && support.Details["user-namespaces"] == "" {
		t.Fatalf("missing diagnostic: %+v", support)
	}
	if !support.CgroupPIDs {
		hasDetail := false
		for _, key := range []string{"cgroup-pids", "cgroup-delegation", "cgroup-v2"} {
			if support.Details[key] != "" {
				hasDetail = true
			}
		}
		if !hasDetail {
			t.Fatalf("missing cgroup diagnostic: %+v", support)
		}
	}
}

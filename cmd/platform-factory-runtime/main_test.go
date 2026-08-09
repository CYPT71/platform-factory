//go:build linux && amd64

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CYPT71/secure-oci-base/internal/ociruntime"
)

func TestNormalizeInvocationAndSignals(t *testing.T) {
	args, err := normalizeInvocation([]string{
		"--root", "/tmp/state", "--log", "/tmp/log", "--systemd-cgroup", "state", "id",
	})
	if err != nil || len(args) != 4 || args[0] != "state" || args[1] != "--root" {
		t.Fatalf("args=%v err=%v", args, err)
	}
	if _, err := normalizeInvocation([]string{"--unknown", "state", "id"}); err == nil {
		t.Fatal("unknown global option accepted")
	}
	for input, want := range map[string]syscall.Signal{
		"TERM": syscall.SIGTERM, "SIGKILL": syscall.SIGKILL, "2": syscall.Signal(2),
	} {
		got, err := parseSignal(input)
		if err != nil || got != want {
			t.Fatalf("%s: got=%v err=%v", input, got, err)
		}
	}
	if _, err := parseSignal("NOPE"); err == nil {
		t.Fatal("unknown signal accepted")
	}
}

func TestDefaultStateRootUsesPrivateRuntimeDirectory(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "runtime"))
	if got, want := defaultStateRoot(), filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "platform-factory-runtime"); got != want {
		t.Fatalf("default root=%q want=%q", got, want)
	}
	t.Setenv("XDG_RUNTIME_DIR", "relative")
	if got, want := defaultStateRoot(), filepath.Join("/run/user", strconv.Itoa(os.Geteuid()), "platform-factory-runtime"); got != want {
		t.Fatalf("fallback root=%q want=%q", got, want)
	}
}

func TestContainerdRuncV2PreStartContract(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	bundle := filepath.Join(t.TempDir(), "bundle")
	if err := os.MkdirAll(filepath.Join(bundle, "rootfs"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{"ociVersion":"1.2.0","root":{"path":"rootfs"},"process":{"user":{"uid":65532,"gid":65532},"args":["/service"],"cwd":"/"}}`
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	previous := launchSupervisor
	t.Cleanup(func() { launchSupervisor = previous })
	var launchedID, launchedPIDFile string
	launchSupervisor = func(_ context.Context, _ *ociruntime.Store, id, executable, pidFile string) error {
		launchedID, launchedPIDFile = id, pidFile
		if !filepath.IsAbs(executable) {
			t.Errorf("supervisor executable is not absolute: %q", executable)
		}
		return nil
	}

	// This is the option ordering emitted by containerd's runc-v2 shim:
	// global logging/root options precede the OCI command and command options.
	var stdout, stderr bytes.Buffer
	if err := runWithIO(context.Background(), []string{
		"--root", root, "--log", filepath.Join(root, "runtime.log"),
		"--log-format", "json", "--systemd-cgroup",
		"create", "--bundle", bundle, "--pid-file", filepath.Join(root, "task.pid"), "sandbox-123",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("containerd create: %v (%s)", err, stderr.String())
	}
	if launchedID != "sandbox-123" || launchedPIDFile != filepath.Join(root, "task.pid") {
		t.Fatalf("launch id=%q pid-file=%q", launchedID, launchedPIDFile)
	}

	stdout.Reset()
	if err := runWithIO(context.Background(), []string{
		"--root", root, "state", "sandbox-123",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("state: %v", err)
	}
	var state ociruntime.State
	if err := json.Unmarshal(stdout.Bytes(), &state); err != nil {
		t.Fatalf("decode state %q: %v", stdout.String(), err)
	}
	if state.ID != "sandbox-123" || state.Status != "created" || state.Bundle != bundle {
		t.Fatalf("unexpected state: %+v", state)
	}

	stdout.Reset()
	if err := runWithIO(context.Background(), []string{
		"--root", root, "list", "--format", "json",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("list: %v", err)
	}
	var states []ociruntime.State
	if err := json.Unmarshal(stdout.Bytes(), &states); err != nil {
		t.Fatalf("decode list %q: %v", stdout.String(), err)
	}
	if len(states) != 1 || states[0].ID != "sandbox-123" {
		t.Fatalf("unexpected list: %+v", states)
	}
	if err := runWithIO(context.Background(), []string{
		"--root", root, "list", "--format", "table",
	}, &stdout, &stderr); err == nil {
		t.Fatal("unsupported list format accepted")
	}

	if err := runWithIO(context.Background(), []string{
		"--root", root, "delete", "--force", "sandbox-123",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("delete --force: %v", err)
	}
	if err := runWithIO(context.Background(), []string{
		"--root", root, "state", "sandbox-123",
	}, &stdout, &stderr); err == nil {
		t.Fatal("state succeeded after delete")
	}
}

func TestContainerdUnsupportedOptionsFailBeforeCreatingState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	for _, args := range [][]string{
		{"--root", root, "create", "--bundle", "/unused", "--console-socket", "/tmp/console", "id"},
		{"--root", root, "create", "--bundle", "/unused", "--preserve-fds", "1", "id"},
		{"--root", root, "create", "--bundle", "/unused", "--no-pivot", "id"},
		{"--root", root, "delete", "--all", "id"},
	} {
		if err := runWithIO(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("unsupported containerd invocation %q succeeded", args)
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("state root was created before option rejection: %v", err)
	}
}

func TestRunCoversMetadataAndFailClosedDispatch(t *testing.T) {
	if err := run(context.Background(), []string{"--version"}); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"features"}); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), nil); err == nil {
		t.Fatal("missing command accepted")
	}
	root := filepath.Join(t.TempDir(), "state")
	if err := run(context.Background(), []string{"--root", root, "delete", "missing"}); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
	for _, args := range [][]string{
		{"--root", root, "state", "missing"},
		{"--root", root, "create", "missing"},
		{"--root", root, "unsupported", "id"},
		{"--root", root, "kill", "missing", "TERM"},
	} {
		if err := run(context.Background(), args); err == nil {
			t.Fatalf("args %v unexpectedly succeeded", args)
		}
	}
}

func TestRunAcceptsContainerdKillAllShape(t *testing.T) {
	root := t.TempDir()
	err := run(context.Background(), []string{
		"kill", "--root", root, "--all", "missing", "SIGKILL",
	})
	if err == nil || !strings.Contains(err.Error(), `container "missing" does not exist`) {
		t.Fatalf("kill --all error = %v", err)
	}
}

func TestRunWaitRejectsMissingContainer(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	var stdout, stderr bytes.Buffer
	err := runWithIO(context.Background(), []string{"--root", root, "wait", "missing"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), `container "missing" does not exist`) {
		t.Fatalf("wait missing error = %v", err)
	}
}

// TestRunWaitReportsExitAfterCrashReconciliation proves `wait` picks up a
// crashed (not just gracefully stopped) supervisor: Store.Get already
// reconciles a dead PID into status "stopped" on every read (see
// internal/ociruntime/runtime.go's getUnlocked), so `wait`'s poll loop
// needs no separate crash-detection logic of its own - the very next
// poll after the PID dies already observes "stopped".
func TestRunWaitReportsExitAfterCrashReconciliation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	bundle := filepath.Join(t.TempDir(), "bundle")
	if err := os.MkdirAll(filepath.Join(bundle, "rootfs"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{"ociVersion":"1.2.0","root":{"path":"rootfs"},"process":{"user":{"uid":65532,"gid":65532},"args":["/service"],"cwd":"/"}}`
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := ociruntime.OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.Create(ctx, "waiter", bundle); err != nil {
		t.Fatal(err)
	}
	// Order matters: SetStatus itself reconciles (via getUnlocked) before
	// applying the requested status, so setting "running" while PID is
	// still 0 - and only then setting the (already-dead) PID - is the
	// only way through the public API to persist "running" with a PID
	// that's dead the moment anything next reads this state. Reversing
	// the order would have SetStatus's own reconciliation see the dead
	// PID first and zero it out before "running" is ever persisted -
	// the exact bug this ordering avoids.
	if err := store.SetStatus(ctx, "waiter", "running"); err != nil {
		t.Fatal(err)
	}
	const deadPID = 1 << 30
	if err := store.SetSupervisor(ctx, "waiter", deadPID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	var stdout, stderr bytes.Buffer
	go func() {
		done <- runWithIO(context.Background(), []string{"--root", root, "wait", "waiter"}, &stdout, &stderr)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait: %v (%s)", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return after the supervisor's PID was already dead")
	}

	var state ociruntime.State
	if err := json.Unmarshal(stdout.Bytes(), &state); err != nil {
		t.Fatalf("decode wait output %q: %v", stdout.String(), err)
	}
	if state.Status != "stopped" || state.PID != 0 {
		t.Fatalf("state=%+v, want stopped with PID cleared", state)
	}
}

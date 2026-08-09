package executor

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/core"
)

// TestMain is required by MaybeApplyRlimitHelper's documented contract:
// this test binary itself uses Executor for memory-limited stages, so it
// must call the hook exactly as any other consumer's main() would.
func TestMain(m *testing.M) {
	MaybeApplyRlimitHelper()
	MaybeApplySandboxHelper(func(context.Context, *net.UDPConn, net.Conn) error { return nil })
	os.Exit(m.Run())
}

func TestMapPath(t *testing.T) {
	cases := map[string]struct{ root, abstractPath, want string }{
		"empty is root": {"/r", "", "/r"},
		"absolute path": {"/r", "/out/bin", "/r/out/bin"},
		"root only":     {"/r", "/", "/r"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := MapPath(tc.root, tc.abstractPath); got != tc.want {
				t.Fatalf("MapPath(%q, %q) = %q, want %q", tc.root, tc.abstractPath, got, tc.want)
			}
		})
	}
}

func TestEffectiveNetwork(t *testing.T) {
	if got := effectiveNetwork(""); got != core.NetworkNone {
		t.Fatalf("got %q", got)
	}
	if got := effectiveNetwork(core.NetworkFull); got != core.NetworkFull {
		t.Fatalf("got %q", got)
	}
}

func TestExecutorConfigurationIsChainable(t *testing.T) {
	executor := New(t.TempDir(), nil)
	resolver := configResolver{"token": []byte("secret")}
	forwarder := &testNetworkRelay{upstream: netip.MustParseAddrPort("127.0.0.1:53")}
	if executor.WithSecretResolver(resolver) != executor || executor.secrets == nil {
		t.Fatal("secret resolver configuration is not chainable")
	}
	if executor.WithDNSForwarder(forwarder) != executor || executor.dnsForwarder != forwarder {
		t.Fatal("DNS forwarder configuration is not chainable")
	}
}

type testNetworkRelay struct {
	upstream netip.AddrPort
}

func (*testNetworkRelay) ServeRelay(context.Context, net.Conn) error { return nil }
func (r *testNetworkRelay) GetUpstream() netip.AddrPort              { return r.upstream }
func (*testNetworkRelay) GetTimeout() int64                          { return 0 }
func (*testNetworkRelay) GetMaxInflight() int                        { return 0 }

var _ core.NetworkRelay = (*testNetworkRelay)(nil)

type configResolver map[string][]byte

func (r configResolver) Resolve(_ context.Context, id string) ([]byte, error) {
	return r[id], nil
}

func TestBoundedBufferTruncates(t *testing.T) {
	var buf boundedBuffer
	if _, err := buf.Write(bytes.Repeat([]byte("a"), maxCapturedBytes+1000)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := len(buf.Bytes()); got != maxCapturedBytes {
		t.Fatalf("captured %d bytes, want %d", got, maxCapturedBytes)
	}
	// A second write past the cap must not grow the buffer or error.
	if _, err := buf.Write([]byte("more")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := len(buf.Bytes()); got != maxCapturedBytes {
		t.Fatalf("captured %d bytes after overflow write, want %d", got, maxCapturedBytes)
	}
}

func TestRunRejectsNonNoneNetwork(t *testing.T) {
	e := New(t.TempDir(), nil)
	stage := core.Stage{ID: "s", Command: core.Command{Executable: "true"}, Network: core.NetworkFull}
	err := e.Run(context.Background(), stage)
	if err == nil || !strings.Contains(err.Error(), "network policy") {
		t.Fatalf("err=%v", err)
	}
	results := e.Results()
	if len(results) != 1 || results[0].ExitCode != -1 || results[0].Err == "" {
		t.Fatalf("results=%+v", results)
	}
}

func TestRunRejectsCPULimit(t *testing.T) {
	e := New(t.TempDir(), nil)
	stage := core.Stage{
		ID: "s", Command: core.Command{Executable: "true"},
		Resources: core.ResourceLimits{CPUMilli: 500},
	}
	if err := e.Run(context.Background(), stage); err == nil || !strings.Contains(err.Error(), "CPU limit") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunRejectsPIDsLimit(t *testing.T) {
	e := New(t.TempDir(), nil)
	stage := core.Stage{
		ID: "s", Command: core.Command{Executable: "true"},
		Resources: core.ResourceLimits{PIDs: 4},
	}
	if err := e.Run(context.Background(), stage); err == nil || !strings.Contains(err.Error(), "process-count limit") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunCapturesOutputExitCodeAndDuration(t *testing.T) {
	e := New(t.TempDir(), []string{"PATH=/usr/bin:/bin"})
	stage := core.Stage{
		ID:      "echo",
		Command: core.Command{Executable: "sh", Args: []string{"-c", "echo out; echo err >&2; exit 0"}},
	}
	if err := e.Run(context.Background(), stage); err != nil {
		t.Fatalf("run: %v", err)
	}
	results := e.Results()
	if len(results) != 1 {
		t.Fatalf("results=%+v", results)
	}
	r := results[0]
	if r.ExitCode != 0 || string(r.Stdout) != "out\n" || string(r.Stderr) != "err\n" {
		t.Fatalf("result=%+v", r)
	}
	if r.Duration <= 0 || r.Started.IsZero() {
		t.Fatalf("result=%+v", r)
	}
}

func TestRunRecordsNonZeroExitCode(t *testing.T) {
	e := New(t.TempDir(), nil)
	stage := core.Stage{ID: "fail", Command: core.Command{Executable: "sh", Args: []string{"-c", "exit 7"}}}
	err := e.Run(context.Background(), stage)
	if err == nil {
		t.Fatal("expected an error for a non-zero exit")
	}
	results := e.Results()
	if len(results) != 1 || results[0].ExitCode != 7 {
		t.Fatalf("results=%+v", results)
	}
}

func TestRunHonorsContextCancellation(t *testing.T) {
	e := New(t.TempDir(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stage := core.Stage{ID: "s", Command: core.Command{Executable: "sh", Args: []string{"-c", "sleep 1"}}}
	if err := e.Run(ctx, stage); err == nil {
		t.Fatal("expected an error for an already-canceled context")
	}
}

func TestRunRecordsExecutableStartFailure(t *testing.T) {
	executor := New(t.TempDir(), nil)
	err := executor.Run(context.Background(), core.Stage{
		ID:      "missing-executable",
		Command: core.Command{Executable: "/definitely/not/a/platform-factory-command"},
	})
	if err == nil || !strings.Contains(err.Error(), "start stage") {
		t.Fatalf("err=%v", err)
	}
	results := executor.Results()
	if len(results) != 1 || results[0].ExitCode != -1 || !strings.Contains(results[0].Err, "start stage") {
		t.Fatalf("results=%+v", results)
	}
}

func TestRunMapsWorkingDirUnderRoot(t *testing.T) {
	root := t.TempDir()
	e := New(root, nil)
	stage := core.Stage{
		ID:      "pwd",
		Command: core.Command{Executable: "pwd", WorkingDir: "/"},
	}
	if err := e.Run(context.Background(), stage); err != nil {
		t.Fatalf("run: %v", err)
	}
	results := e.Results()
	if got := strings.TrimSpace(string(results[0].Stdout)); got != root {
		// On macOS, TMPDIR paths can resolve through a /private symlink;
		// only fail if the reported directory is neither the root nor its
		// symlink-resolved form.
		if !strings.HasSuffix(got, root) {
			t.Fatalf("pwd=%q, want %q", got, root)
		}
	}
}

func TestRunPassesStageEnvNotHostEnv(t *testing.T) {
	e := New(t.TempDir(), []string{"PATH=/usr/bin:/bin"})
	stage := core.Stage{
		ID:      "env",
		Command: core.Command{Executable: "sh", Args: []string{"-c", "echo \"$GREETING\""}},
		Env:     map[string]string{"GREETING": "hi"},
	}
	if err := e.Run(context.Background(), stage); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.TrimSpace(string(e.Results()[0].Stdout)); got != "hi" {
		t.Fatalf("stdout=%q", got)
	}
}

func TestRunEnforcesMemoryCeilingOnChild(t *testing.T) {
	if !resourceLimitsSupported() {
		t.Skip("resource limits are not supported on this platform")
	}
	e := New(t.TempDir(), []string{"PATH=/usr/bin:/bin"})
	const limitMiB = 256
	stage := core.Stage{
		ID:        "ulimit",
		Command:   core.Command{Executable: "sh", Args: []string{"-c", "ulimit -v"}},
		Resources: core.ResourceLimits{MemoryMiB: limitMiB},
	}
	if err := e.Run(context.Background(), stage); err != nil {
		t.Fatalf("run: %v", err)
	}
	reportedKB, err := strconv.ParseInt(strings.TrimSpace(string(e.Results()[0].Stdout)), 10, 64)
	if err != nil {
		t.Fatalf("parse ulimit -v output: %v", err)
	}
	wantKB := int64(limitMiB) << 10
	if reportedKB != wantKB {
		t.Fatalf("child inherited RLIMIT_AS=%d KB, want %d KB", reportedKB, wantKB)
	}
}

func TestRunConcurrentMemoryLimitedStagesDoNotRace(t *testing.T) {
	if !resourceLimitsSupported() {
		t.Skip("resource limits are not supported on this platform")
	}
	e := New(t.TempDir(), []string{"PATH=/usr/bin:/bin"})
	done := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func(i int) {
			// Each memory-limited stage re-execs into its own fresh child
			// process (see rlimit_linux.go); no shared process-wide rlimit
			// state is touched, so this value is just a realistic limit,
			// not a workaround for cross-goroutine interference.
			stage := core.Stage{
				// Stage IDs address per-stage sandbox/cgroup state and therefore
				// must be unique, exactly as they are in a validated pipeline.
				ID:      fmt.Sprintf("concurrent-%d", i),
				Command: core.Command{Executable: "sh", Args: []string{"-c", "sleep 0.01"}},
				// Generous ceiling: the re-exec helper is this test binary,
				// whose runtime needs real address space; a tight limit made
				// this test flake under full-suite load.
				Resources: core.ResourceLimits{MemoryMiB: 512},
			}
			done <- e.Run(context.Background(), stage)
		}(i)
	}
	for i := 0; i < 4; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent run: %v", err)
		}
	}
	if len(e.Results()) != 4 {
		t.Fatalf("results=%d", len(e.Results()))
	}
}

func TestNewDefaultsBaseEnv(t *testing.T) {
	e := New("/root", nil)
	want := []string{
		"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TZ=UTC", "SOURCE_DATE_EPOCH=0",
	}
	if !reflect.DeepEqual(e.baseEnv, want) {
		t.Fatalf("baseEnv=%v", e.baseEnv)
	}
}

func TestStageEnvironmentPublishesResolvedRoot(t *testing.T) {
	e := New("/resolved/stage-root", nil)
	env := e.stageEnv(core.Stage{})
	if !slices.Contains(env, "PLATFORM_FACTORY_ROOT=/resolved/stage-root") {
		t.Fatalf("stage environment does not publish its root: %v", env)
	}
}

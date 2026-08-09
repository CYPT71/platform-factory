//go:build !linux

package executor

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/core"
)

type failingSecretResolver struct{}

func (failingSecretResolver) Resolve(context.Context, string) ([]byte, error) {
	return nil, errors.New("secret backend unavailable")
}

func TestProbeSandboxReportsNonLinuxCapabilitiesUnavailable(t *testing.T) {
	support := ProbeSandbox()
	if support.UserNamespaces || support.CgroupPIDs || support.CgroupCPU {
		t.Fatalf("non-Linux sandbox reported available: %+v", support)
	}
	if support.Details["user-namespaces"] == "" || support.Details["cgroup-v2"] == "" {
		t.Fatalf("missing actionable support details: %+v", support.Details)
	}
}

func TestNonLinuxIsolationPrimitivesFailClosed(t *testing.T) {
	if host, child, err := dnsSocketPair(); err == nil || host != nil || child != nil {
		t.Fatalf("dnsSocketPair=(%v,%v,%v), want unavailable", host, child, err)
	}
	if err := wrapWithSandboxHelper(exec.Command("true"), sandboxHelperPayload{}, false, true); err == nil {
		t.Fatal("sandbox helper unexpectedly accepted")
	}
	if group, err := newStageCgroup("", exec.Command("true"), 1, 1); err == nil || group != nil {
		t.Fatalf("newStageCgroup=(%v,%v), want unavailable", group, err)
	}
	(&stageCgroup{}).cleanup()
	MaybeApplySandboxHelper(func(context.Context, *net.UDPConn, net.Conn) error { return nil })
	MaybeApplyRlimitHelper()
}

func TestNonLinuxRlimitWrapperCannotBeUsed(t *testing.T) {
	if resourceLimitsSupported() {
		t.Fatal("resource limits unexpectedly supported")
	}
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("wrapWithRlimitHelper did not fail closed")
		}
	}()
	_ = wrapWithRlimitHelper(exec.Command("true"), 1024)
}

func TestSandboxedPreparationRejectsUnsupportedRequirements(t *testing.T) {
	base := func() *Executor {
		executor, err := NewSandboxed(t.TempDir(), nil, SandboxSupport{
			UserNamespaces: true,
			Details: map[string]string{
				"cgroup-pids": "pids controller unavailable",
				"cgroup-cpu":  "cpu controller unavailable",
			},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return executor
	}
	tests := []struct {
		name string
		edit func(*Executor, *core.Stage)
		want string
	}{
		{"resolve without relay", func(_ *Executor, s *core.Stage) { s.Network = core.NetworkResolve }, "explicit project-owned DNS forwarder"},
		{"resolve without pinned upstream", func(e *Executor, s *core.Stage) {
			s.Network = core.NetworkResolve
			e.WithDNSForwarder(&testNetworkRelay{})
		}, "explicit upstream address"},
		{"pids without controller", func(_ *Executor, s *core.Stage) { s.Resources.PIDs = 2 }, "pids controller unavailable"},
		{"cpu without controller", func(_ *Executor, s *core.Stage) { s.Resources.CPUMilli = 500 }, "cpu controller unavailable"},
		{"unknown mount source", func(_ *Executor, s *core.Stage) {
			s.Mounts = []core.Mount{{Source: "missing", Target: "/input", ReadOnly: true}}
		}, "no resolved host source"},
		{"secret without resolver", func(_ *Executor, s *core.Stage) {
			s.Secrets = []core.SecretReference{{ID: "token", Target: "/run/token"}}
		}, "no secret resolver"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			executor := base()
			stage := core.Stage{ID: "secure", Command: core.Command{Executable: "true"}}
			tc.edit(executor, &stage)
			_, _, _, _, err := executor.prepareSandboxed(context.Background(), stage)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestSandboxedPreparationPropagatesSecretResolutionFailure(t *testing.T) {
	executor, err := NewSandboxed(t.TempDir(), nil, SandboxSupport{UserNamespaces: true, Details: map[string]string{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	executor.WithSecretResolver(failingSecretResolver{})
	_, _, _, redactions, err := executor.prepareSandboxed(context.Background(), core.Stage{
		ID: "secret", Command: core.Command{Executable: "true"},
		Secrets: []core.SecretReference{{ID: "missing", Target: "/run/missing"}},
	})
	if err == nil || !strings.Contains(err.Error(), "secret \"missing\"") || redactions != nil {
		t.Fatalf("redactions=%v err=%v", redactions, err)
	}
}

func TestSandboxedPreparationRejectsPlatformHelperAfterBuildingPayload(t *testing.T) {
	executor, err := NewSandboxed(t.TempDir(), []string{"PATH=/bin"}, SandboxSupport{UserNamespaces: true, Details: map[string]string{}}, map[string]string{"source": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	executor.WithSecretResolver(configResolver{"token": []byte("sentinel")})
	_, _, _, _, err = executor.prepareSandboxed(context.Background(), core.Stage{
		ID: "payload", Command: core.Command{Executable: "true", WorkingDir: "/work"},
		Mounts:  []core.Mount{{Source: "source", Target: "/input", ReadOnly: true}},
		Secrets: []core.SecretReference{{ID: "token", Target: "/run/token"}},
		Sandbox: core.SandboxPolicy{ReadOnlyRoot: true, NonRoot: true},
	})
	if err == nil || !strings.Contains(err.Error(), "sandboxed execution is not available") {
		t.Fatalf("err=%v", err)
	}
}

func TestSupportDetailFallback(t *testing.T) {
	executor := &Executor{support: SandboxSupport{Details: map[string]string{"second": "detail"}}}
	if got := executor.supportDetail("first", "second"); got != "detail" {
		t.Fatalf("got %q", got)
	}
	if got := executor.supportDetail("missing"); !strings.Contains(got, "unavailable") {
		t.Fatalf("got %q", got)
	}
}

func TestNonLinuxExecutorRefusesUnenforceablePolicies(t *testing.T) {
	executor := New(t.TempDir(), nil)
	tests := []struct {
		name  string
		stage core.Stage
		want  string
	}{
		{"memory", core.Stage{ID: "memory", Command: core.Command{Executable: "true"}, Resources: core.ResourceLimits{MemoryMiB: 1}}, "not supported on this platform"},
		{"secret", core.Stage{ID: "secret", Command: core.Command{Executable: "true"}, Secrets: []core.SecretReference{{ID: "token", Target: "/run/token"}}}, "only the sandboxed executor"},
		{"readonly", core.Stage{ID: "readonly", Command: core.Command{Executable: "true"}, Sandbox: core.SandboxPolicy{ReadOnlyRoot: true}}, "no mount or user namespace"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := executor.Run(context.Background(), tc.stage)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
	if got := executor.Results(); len(got) != len(tests) {
		t.Fatalf("results=%d, want %d rejected stages recorded", len(got), len(tests))
	}
}

func TestNonLinuxSandboxRunRecordsPlatformRejection(t *testing.T) {
	executor, err := NewSandboxed(t.TempDir(), nil, SandboxSupport{UserNamespaces: true, Details: map[string]string{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = executor.Run(context.Background(), core.Stage{ID: "sandbox", Command: core.Command{Executable: "true"}})
	if err == nil || !strings.Contains(err.Error(), "sandboxed execution is not available") {
		t.Fatalf("err=%v", err)
	}
	results := executor.Results()
	if len(results) != 1 || results[0].ExitCode != -1 || results[0].Err == "" {
		t.Fatalf("results=%+v", results)
	}
}

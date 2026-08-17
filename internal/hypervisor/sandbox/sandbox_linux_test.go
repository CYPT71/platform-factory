//go:build linux

package sandbox

import (
	"testing"
)

// These tests exercise the real syscalls in sandbox_linux.go directly
// (not through Apply's testMode hooks). They run as part of the
// standard `go test ./...` gate too, but most of them skip cleanly
// when the calling process lacks the privilege the real operation
// needs - exactly the failure mode a standard GitHub-hosted runner is
// in. ci-sandbox.yml re-runs this package's tests under a delegated,
// privileged scope (systemd-run --scope --property=Delegate=yes as
// root) the same way it already does for internal/executor, so these
// same tests get real, non-skipped coverage there.

func TestRealApplySeccomp(t *testing.T) {
	// PR_SET_NO_NEW_PRIVS needs no privilege, so this always runs for
	// real, on every runner. It permanently mutates the test binary's
	// own process state (no_new_privs cannot be unset), which is
	// harmless here since nothing later in this test binary execs a
	// setuid or file-capability binary.
	s := NewSandbox(Config{Seccomp: true})
	if err := s.applySeccomp(); err != nil {
		t.Fatalf("applySeccomp: %v", err)
	}
}

func TestRealApplyNamespaces(t *testing.T) {
	support := ProbeSandbox()
	if !support.Namespaces {
		t.Skipf("namespace isolation unavailable: %s", support.Details["namespaces"])
	}
	s := NewSandbox(Config{Namespaces: true})
	if err := s.applyNamespaces(); err != nil {
		t.Fatalf("applyNamespaces: %v", err)
	}
}

func TestRealApplyCgroups(t *testing.T) {
	support := ProbeSandbox()
	if !support.Cgroups {
		t.Skipf("cgroup delegation unavailable: %s", support.Details["cgroups"])
	}
	s := NewSandbox(Config{Cgroups: true, PIDsLimit: 64, CPULimit: 50, MemoryLimit: 128 * 1024 * 1024})
	if err := s.applyCgroups(); err != nil {
		t.Fatalf("applyCgroups: %v", err)
	}
	if s.cgroupPath == "" {
		t.Error("expected cgroupPath to be set after applyCgroups")
	}
	t.Cleanup(func() { _ = s.Cleanup() })
}

func TestRealDropCapability(t *testing.T) {
	support := ProbeSandbox()
	if !support.CapabilityBoundingDrop {
		t.Skipf("capability bounding-set drop unavailable: %s", support.Details["capability-bounding-drop"])
	}
	if err := dropCapability("CAP_SYS_MODULE"); err != nil {
		t.Fatalf("dropCapability: %v", err)
	}
}

func TestDropCapabilityUnknown(t *testing.T) {
	if err := dropCapability("CAP_DOES_NOT_EXIST"); err == nil {
		t.Error("expected dropCapability to reject an unknown capability name")
	}
}

func TestProbeSandboxDoesNotPanic(t *testing.T) {
	support := ProbeSandbox()
	if support.Details == nil {
		t.Error("expected Details map to be initialized")
	}
}

func TestHasCapabilityDoesNotPanic(t *testing.T) {
	// The result depends on the test environment; only the absence of
	// a panic and a well-formed read of /proc/self/status matter here.
	_ = hasCapability(capSysAdmin)
}

// TestDefaultSeccompProfileCompiles and TestApplyStrictSeccompRealFilter
// moved to seccomp_linux_test.go (//go:build linux && amd64) - they
// exercise seccomp_linux.go's linux/amd64-specific classic-BPF filter,
// which does not exist on other architectures (see
// seccomp_linux_other.go).

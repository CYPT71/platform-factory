//go:build linux

package sandbox

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
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

// TestDefaultSeccompProfileCompiles proves every syscall name
// DefaultSeccompProfile lists is one buildSeccompProgram can actually
// resolve and encode into classic BPF - the exact property the profile's
// syscall names need and can silently fail to have without this (a typo'd
// or nonexistent name would only surface once applyStrictSeccomp is
// called for real, which - see TestApplyStrictSeccompRealFilter below -
// only ever happens in a throwaway subprocess).
func TestDefaultSeccompProfileCompiles(t *testing.T) {
	program, err := buildSeccompProgram(DefaultSeccompProfile())
	if err != nil {
		t.Fatalf("DefaultSeccompProfile does not compile: %v", err)
	}
	if len(program) == 0 {
		t.Fatal("expected a non-empty compiled program")
	}
}

// TestApplyStrictSeccompRealFilter proves applyStrictSeccomp's compiled
// filter actually does something, in a throwaway re-exec'd child process
// rather than this shared test binary: installing it here directly would
// permanently restrict whichever OS thread happens to be current to
// DefaultSeccompProfile's allow-list, and the Go scheduler is free to hand
// that same thread to a completely unrelated goroutine afterward - see
// applyStrictSeccomp's own doc comment (seccomp_linux.go). The child
// installs the real filter, then attempts an allowed syscall (getpid) and,
// only if that worked, a disallowed one (mount) that the profile's default
// action kills the process for; the parent asserts on the child's
// observable exit status.
func TestApplyStrictSeccompRealFilter(t *testing.T) {
	if os.Getenv("PLATFORM_FACTORY_SANDBOX_STRICT_SECCOMP_HELPER") == "1" {
		runStrictSeccompHelper()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestApplyStrictSeccompRealFilter$", "-test.v")
	cmd.Env = append(os.Environ(), "PLATFORM_FACTORY_SANDBOX_STRICT_SECCOMP_HELPER=1")
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected the helper to die from its own seccomp filter, got err=%v output=%s", err, output)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGSYS {
		t.Fatalf("expected the helper to be killed by SIGSYS from its own filter, got status=%v output=%s", exitErr, output)
	}
	if !bytesContain(output, "getpid-ok") {
		t.Fatalf("expected the helper to reach and log a successful allowed syscall before the disallowed one killed it, output=%s", output)
	}
}

// runStrictSeccompHelper is the re-exec'd child body for
// TestApplyStrictSeccompRealFilter.
func runStrictSeccompHelper() {
	// applyStrictSeccomp's own doc comment requires the calling goroutine
	// to already be pinned to its OS thread: without this, the write to
	// stdout just below - or any other blocking operation - can resume
	// this goroutine on a different, unfiltered thread, and the mount(2)
	// call meant to prove the filter blocks it would silently run
	// unfiltered instead.
	runtime.LockOSThread()
	s := NewSandbox(Config{})
	if err := s.applyStrictSeccomp(); err != nil {
		os.Stderr.WriteString("applyStrictSeccomp failed: " + err.Error() + "\n")
		os.Exit(2)
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_GETPID, 0, 0, 0); errno != 0 {
		os.Stderr.WriteString("allowed syscall getpid unexpectedly failed: " + errno.Error() + "\n")
		os.Exit(3)
	}
	os.Stdout.WriteString("getpid-ok\n")
	// mount(2) is nowhere in DefaultSeccompProfile's allow-list; the
	// installed filter's default action (SeccompActionKill ->
	// SECCOMP_RET_KILL_PROCESS) means the kernel terminates this process
	// right here, before mount ever executes, with SIGSYS. If it somehow
	// didn't, that is itself the bug this test exists to catch, hence the
	// explicit failure exit right after.
	syscall.Syscall(syscall.SYS_MOUNT, 0, 0, 0)
	os.Stderr.WriteString("mount was not blocked by seccomp\n")
	os.Exit(4)
}

func bytesContain(haystack []byte, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == needle {
			return true
		}
	}
	return false
}

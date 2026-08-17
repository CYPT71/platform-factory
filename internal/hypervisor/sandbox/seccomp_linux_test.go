//go:build linux && amd64

package sandbox

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
)

// These two tests exercise seccomp_linux.go's linux/amd64-specific
// classic-BPF filter directly - see seccomp_linux_other.go for why this
// package's real implementation of applyStrictSeccomp/buildSeccompProgram
// does not extend to other architectures.

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

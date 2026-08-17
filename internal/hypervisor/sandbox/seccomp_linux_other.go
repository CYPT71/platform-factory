//go:build linux && !amd64

package sandbox

import "fmt"

// applyStrictSeccomp's real implementation (seccomp_linux.go) bakes a
// classic-BPF program around linux/amd64-specific syscall numbers and an
// amd64 audit-architecture check - it does not port to another
// architecture by just recomputing a table, since arm64 dropped several
// of the legacy syscalls that table names entirely (access, in favor of
// faccessat; arch_prctl does not exist on arm64 at all). Rather than
// publish unverified syscall-number guesses for a security-sensitive
// filter, this fails closed with a clear error - unlike sandbox_other.go's
// non-Linux no-op (a real host that was never a deployment target for
// this package at all), a real linux/arm64 host IS a plausible
// deployment target, so a caller must not be told strict seccomp
// succeeded when it did not run.
func (s *Sandbox) applyStrictSeccomp() error {
	return fmt.Errorf("sandbox: strict seccomp filtering is only implemented for linux/amd64 (see seccomp_linux.go)")
}

// isSeccompEnabled honestly reports "not enabled" rather than claim
// knowledge this architecture's implementation doesn't have a way to
// check.
func isSeccompEnabled() bool { return false }

//go:build !linux

// RLIMIT_AS is defined on Darwin but the kernel rejects setting it
// (Setrlimit returns EINVAL): macOS's Mach VM does not support an
// address-space ceiling the way Linux does. Treat every non-Linux
// platform, including Darwin, as unsupported rather than pretending
// this enforces anything there.
package executor

import "os/exec"

func resourceLimitsSupported() bool { return false }

func wrapWithRlimitHelper(*exec.Cmd, int64) error {
	panic("executor: wrapWithRlimitHelper must not be called when resourceLimitsSupported is false")
}

// MaybeApplyRlimitHelper is a no-op on this platform: resource limits are
// never wrapped here, so there is nothing to intercept. Safe to call
// unconditionally from main() on any platform.
func MaybeApplyRlimitHelper() {}

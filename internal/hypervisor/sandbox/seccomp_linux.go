//go:build linux && amd64

package sandbox

import (
	"fmt"
	"syscall"
	"unsafe"
)

// This file assembles and installs a real classic-BPF seccomp filter using
// only the two syscalls the kernel actually requires for it (prctl(2) for
// PR_SET_NO_NEW_PRIVS/PR_SET_SECCOMP/PR_GET_SECCOMP) - no libseccomp, no
// cgo, matching how internal/hypervisor/kvm already talks to /dev/kvm
// through raw syscall.Syscall calls rather than a helper library.
//
// prctl(PR_SET_SECCOMP, ...) - as opposed to the newer seccomp(2) syscall -
// is what's used here specifically because it only needs
// PR_SET_NO_NEW_PRIVS, not CAP_SYS_ADMIN, so a filter can be installed by an
// already-unprivileged process. A filter installed this way applies only to
// the calling thread (and any thread/process it later creates), not
// retroactively to sibling OS threads already running - which is exactly
// the scoping this package relies on: the caller is expected to have
// already called runtime.LockOSThread on the goroutine that's about to run
// the KVM_RUN loop, the same precondition internal/ociruntime's AppArmor
// confinement (applyApparmorProfile) already documents and relies on.

const (
	// prSetNoNewPrivs is defined in sandbox_linux.go (portable across
	// every Linux architecture, unlike the rest of this block).
	prSetSeccomp      = 22
	prGetSeccomp      = 21
	seccompModeFilter = 2

	// audit.h: EM_X86_64 (0x3E) tagged 64-bit/little-endian. A seccomp
	// filter that skips this check can be bypassed on a kernel that
	// supports x32 or ia32 compat syscall entry points, where the same
	// numeric syscall number means something else - checking it is not
	// optional.
	auditArchX86_64 = 0xC000003E

	// linux/seccomp.h SECCOMP_RET_* actions, shifted so the low 16 bits
	// carry the SECCOMP_RET_DATA (unused for KILL_PROCESS/ALLOW).
	seccompRetKillProcess = 0x80000000
	seccompRetAllow       = 0x7fff0000

	// linux/seccomp.h struct seccomp_data field offsets on every
	// architecture: int nr; __u32 arch; __u64 instruction_pointer;
	// __u64 args[6];
	seccompDataOffNR   = 0
	seccompDataOffArch = 4

	// Classic BPF (linux/filter.h / linux/bpf_common.h) opcodes needed to
	// express "compare a loaded word against a constant and branch".
	bpfLd  = 0x00
	bpfW   = 0x00
	bpfAbs = 0x20
	bpfJmp = 0x05
	bpfJeq = 0x10
	bpfK   = 0x00
	bpfRet = 0x06

	// jt/jf are single bytes: an allow-list longer than this can't be
	// encoded as a flat compare chain without risking an overflowing
	// jump offset, so buildSeccompProgram refuses to build one instead
	// of silently emitting a broken filter.
	maxSeccompAllowedSyscalls = 255
)

// sockFilter mirrors struct sock_filter (linux/filter.h) byte-for-byte.
type sockFilter struct {
	code uint16
	jt   uint8
	jf   uint8
	k    uint32
}

// sockFprog mirrors struct sock_fprog (linux/filter.h) byte-for-byte; it is
// what PR_SET_SECCOMP expects a pointer to.
type sockFprog struct {
	len    uint16
	_      [6]byte // padding to align the pointer field on amd64
	filter *sockFilter
}

func bpfStmt(code uint16, k uint32) sockFilter { return sockFilter{code: code, k: k} }

func bpfJump(code uint16, k uint32, jt, jf uint8) sockFilter {
	return sockFilter{code: code, jt: jt, jf: jf, k: k}
}

// buildSeccompProgram compiles profile into a classic-BPF program that:
//  1. kills the process outright if it is not running as a native x86_64
//     syscall (blocks the x32/ia32 compat entry points wholesale, since
//     nrToNames only covers native x86_64 numbers),
//  2. allows exactly the syscalls named in profile.AllowedSyscalls, and
//  3. applies profile.DefaultAction to everything else.
func buildSeccompProgram(profile SeccompProfile) ([]sockFilter, error) {
	numbers := make([]uint32, 0, len(profile.AllowedSyscalls))
	seen := make(map[uint32]bool, len(profile.AllowedSyscalls))
	for _, name := range profile.AllowedSyscalls {
		nr, ok := syscallNumberX8664[name]
		if !ok {
			return nil, fmt.Errorf("sandbox: unknown x86_64 syscall name %q in seccomp profile", name)
		}
		if seen[nr] {
			continue
		}
		seen[nr] = true
		numbers = append(numbers, nr)
	}
	if len(numbers) > maxSeccompAllowedSyscalls {
		return nil, fmt.Errorf("sandbox: seccomp allow-list has %d syscalls, more than the %d a flat compare chain can encode", len(numbers), maxSeccompAllowedSyscalls)
	}
	defaultRet, err := seccompActionValue(profile.DefaultAction)
	if err != nil {
		return nil, err
	}

	program := make([]sockFilter, 0, len(numbers)+6)
	program = append(program,
		bpfStmt(bpfLd|bpfW|bpfAbs, seccompDataOffArch),
		bpfJump(bpfJmp|bpfJeq|bpfK, auditArchX86_64, 1, 0),
		bpfStmt(bpfRet|bpfK, seccompRetKillProcess),
		bpfStmt(bpfLd|bpfW|bpfAbs, seccompDataOffNR),
	)
	for i, nr := range numbers {
		// jt jumps past the remaining (len-1-i) compares plus the
		// default-action RET, landing on the ALLOW RET appended after
		// the loop; jf falls through to the next compare (or, for the
		// last one, straight into the default-action RET).
		jt := uint8(len(numbers) - i)
		program = append(program, bpfJump(bpfJmp|bpfJeq|bpfK, nr, jt, 0))
	}
	program = append(program,
		bpfStmt(bpfRet|bpfK, defaultRet),
		bpfStmt(bpfRet|bpfK, seccompRetAllow),
	)
	return program, nil
}

func seccompActionValue(action SeccompAction) (uint32, error) {
	switch action {
	case SeccompActionKill:
		return seccompRetKillProcess, nil
	case SeccompActionAllow:
		return seccompRetAllow, nil
	case SeccompActionErrno:
		// SECCOMP_RET_ERRNO | EPERM: reject with EPERM rather than
		// terminate. Not used by DefaultSeccompProfile but kept
		// available for callers that build their own SeccompProfile.
		return 0x00050000 | uint32(syscall.EPERM), nil
	case SeccompActionTrap:
		return 0x00030000, nil
	default:
		return 0, fmt.Errorf("sandbox: unsupported seccomp default action %q", action)
	}
}

// installSeccompFilter loads program into the kernel for the calling
// thread via prctl(PR_SET_SECCOMP, SECCOMP_MODE_FILTER, ...), first setting
// PR_SET_NO_NEW_PRIVS so the installation does not require CAP_SYS_ADMIN.
func installSeccompFilter(program []sockFilter) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0); errno != 0 {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", errno)
	}
	prog := sockFprog{len: uint16(len(program)), filter: &program[0]}
	if _, _, errno := syscall.Syscall(syscall.SYS_PRCTL, prSetSeccomp, seccompModeFilter, uintptr(unsafe.Pointer(&prog))); errno != 0 {
		return fmt.Errorf("prctl(PR_SET_SECCOMP): %w", errno)
	}
	return nil
}

// applyStrictSeccomp compiles DefaultSeccompProfile and installs it on the
// calling OS thread as a real classic-BPF filter - unlike applySeccomp
// (sandbox_linux.go), which only sets PR_SET_NO_NEW_PRIVS and is safe to
// call from anywhere at any time, this permanently and irreversibly
// restricts every future syscall this thread makes to the profile's
// allow-list. It must only be called from a goroutine already
// runtime.LockOSThread-pinned to the OS thread that is about to do the
// security-sensitive work the filter is meant to protect (see
// internal/ociruntime/supervisor_linux.go's ServeSupervisor, the one
// caller): called from an unpinned goroutine, the Go scheduler can migrate
// that goroutine to a different, unfiltered thread immediately afterward,
// or migrate a *different*, ordinary goroutine onto the now-filtered
// thread, killing it the moment it makes any syscall the profile doesn't
// list. sandbox_linux_test.go's real coverage of this exercises it only in
// a throwaway re-exec'd subprocess for exactly that reason.
func (s *Sandbox) applyStrictSeccomp() error {
	program, err := buildSeccompProgram(DefaultSeccompProfile())
	if err != nil {
		return err
	}
	return installSeccompFilter(program)
}

// isSeccompEnabled reports whether the calling thread already has a
// seccomp filter installed, via prctl(PR_GET_SECCOMP) (returns 0 = disabled,
// 1 = strict, 2 = filter - anything nonzero means some filter is active).
func isSeccompEnabled() bool {
	mode, _, errno := syscall.Syscall(syscall.SYS_PRCTL, prGetSeccomp, 0, 0)
	return errno == 0 && mode != 0
}

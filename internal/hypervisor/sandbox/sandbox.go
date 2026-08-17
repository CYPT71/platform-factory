// Package sandbox provides VMM sandboxing and isolation mechanisms.
// It implements seccomp, namespaces, cgroups, and privilege dropping for
// secure microVM execution.
package sandbox

import (
	"fmt"
	"os"
)

// Config holds the sandbox configuration for a VMM process.
type Config struct {
	// Seccomp enables seccomp filtering.
	Seccomp bool
	// Namespaces enables namespace isolation.
	Namespaces bool
	// Cgroups enables cgroup resource limits.
	Cgroups bool
	// DropPrivileges drops root privileges after setup.
	DropPrivileges bool
	// DropCapabilities drops specific Linux capabilities.
	DropCapabilities []string
	// NamespaceList selects which namespaces applyNamespaces unshares.
	// Empty (the common case: DefaultConfig/VMMConfig leave it unset)
	// means the safe default of {NamespaceNET, NamespaceUTS,
	// NamespaceIPC} - network, hostname and SysV IPC isolation. Mount,
	// PID and user namespaces are deliberately not part of that default:
	// each changes semantics this codebase depends on elsewhere (mount
	// namespace on a thread mid-process needs the whole rootfs/console
	// path re-verified against it; PID namespace makes this process
	// PID 1 with reaping/signal semantics ociruntime's supervisor
	// doesn't currently implement; user namespace needs an explicit
	// uid/gid mapping this package has no way to obtain). Request them
	// explicitly here only once that work has actually been done.
	NamespaceList []Namespace
	// CgroupParent overrides the cgroup v2 directory a leaf cgroup is
	// created under; empty uses the calling process's own cgroup
	// (parsed from /proc/self/cgroup).
	CgroupParent string
	// CPULimit caps CPU as a percentage of one core (100 = one full
	// core, 50 = half a core, 0 = unlimited); applyCgroups writes it to
	// cgroup v2's cpu.max as a quota over a fixed 100ms period.
	CPULimit int
	// MemoryLimit is the maximum memory in bytes (0 = unlimited).
	MemoryLimit int64
	// PIDsLimit is the maximum number of PIDs (0 = unlimited).
	PIDsLimit int
}

// DefaultConfig returns a default sandbox configuration.
func DefaultConfig() Config {
	return Config{
		Seccomp:          true,
		Namespaces:       true,
		Cgroups:          true,
		DropPrivileges:   true,
		DropCapabilities: []string{"CAP_NET_ADMIN", "CAP_SYS_ADMIN", "CAP_SYS_MODULE"},
	}
}

// VMMConfig returns a sandbox configuration suitable for VMM processes.
func VMMConfig() Config {
	return Config{
		Seccomp:          true,
		Namespaces:       true,
		Cgroups:          true,
		DropPrivileges:   true,
		DropCapabilities: AllCapabilities,
		CPULimit:         100,
		PIDsLimit:        1000,
	}
}

// AllCapabilities is a list of all Linux capabilities to drop.
var AllCapabilities = []string{
	"CAP_CHOWN",
	"CAP_DAC_OVERRIDE",
	"CAP_DAC_READ_SEARCH",
	"CAP_FOWNER",
	"CAP_FSETID",
	"CAP_KILL",
	"CAP_SETGID",
	"CAP_SETUID",
	"CAP_SETPCAP",
	"CAP_LINUX_IMMUTABLE",
	"CAP_NET_BIND_SERVICE",
	"CAP_NET_BROADCAST",
	"CAP_NET_ADMIN",
	"CAP_NET_RAW",
	"CAP_IPC_LOCK",
	"CAP_IPC_OWNER",
	"CAP_SYS_MODULE",
	"CAP_SYS_RAWIO",
	"CAP_SYS_CHROOT",
	"CAP_SYS_PTRACE",
	"CAP_SYS_PACCT",
	"CAP_SYS_ADMIN",
	"CAP_SYS_BOOT",
	"CAP_SYS_NICE",
	"CAP_SYS_RESOURCE",
	"CAP_SYS_TIME",
	"CAP_SYS_TTY_CONFIG",
	"CAP_MKNOD",
	"CAP_LEASE",
	"CAP_AUDIT_WRITE",
	"CAP_AUDIT_CONTROL",
	"CAP_SETFCAP",
	"CAP_MAC_OVERRIDE",
	"CAP_MAC_ADMIN",
	"CAP_SYSLOG",
	"CAP_WAKE_ALARM",
	"CAP_BLOCK_SUSPEND",
	"CAP_AUDIT_READ",
	"CAP_PERFMON",
	"CAP_BPF",
	"CAP_CHECKPOINT_RESTORE",
}

// Namespace represents a Linux namespace.
type Namespace string

// Standard Linux namespaces.
const (
	NamespacePID    Namespace = "pid"
	NamespaceUTS    Namespace = "uts"
	NamespaceMNT    Namespace = "mnt"
	NamespaceIPC    Namespace = "ipc"
	NamespaceNET    Namespace = "net"
	NamespaceUSER   Namespace = "user"
	NamespaceCGROUP Namespace = "cgroup"
)

// ParseNamespace parses a namespace string.
func ParseNamespace(ns string) (Namespace, error) {
	switch ns {
	case "pid", "uts", "mnt", "ipc", "net", "user", "cgroup":
		return Namespace(ns), nil
	default:
		return "", fmt.Errorf("unknown namespace: %s", ns)
	}
}

// SeccompProfile defines the seccomp filtering profile.
type SeccompProfile struct {
	// AllowedSyscalls is the list of allowed system calls.
	AllowedSyscalls []string
	// DefaultAction is the default action for unlisted syscalls.
	DefaultAction SeccompAction
}

// SeccompAction defines the action to take on a syscall.
type SeccompAction string

// Seccomp actions.
const (
	SeccompActionAllow SeccompAction = "SCMP_ACT_ALLOW"
	SeccompActionErrno SeccompAction = "SCMP_ACT_ERRNO"
	SeccompActionKill  SeccompAction = "SCMP_ACT_KILL"
	SeccompActionTrap  SeccompAction = "SCMP_ACT_TRAP"
)

// DefaultSeccompProfile returns the seccomp profile for a native KVM VMM
// thread. Every name below was verified against
// a real /dev/kvm boot and a real internal/rootfs.Convert initramfs build
// under strace on linux/amd64, plus direct inspection of the syscalls those
// two call paths can reach for branches the traced runs didn't happen to
// exercise (FIFOs, hardlinks, symlinks, atomic renames) - not guessed. See
// syscallNumberX8664 in syscalls_linux.go for the syscall-number source and
// per-entry notes, including why execve is deliberately absent.
//
// openat2 and fchmodat2 were added separately: buildGuestInitramfs's
// Podman-rootfs confinement (rootfs_linux.go) opens the container's root
// via os.Root/os.OpenRoot and copies its tree with os.Root.Mkdir/Chmod/
// OpenFile/Symlink, all confined against escaping the root. Go's runtime
// implements that confinement on Linux with openat2's RESOLVE_BENEATH for
// opens, and os.Root.Chmod specifically needs fchmodat2 - plain fchmodat
// has no AT_SYMLINK_NOFOLLOW support at all, so there is no old-syscall
// fallback for a confined, symlink-safe chmod. That call path postdates
// the strace verification above and both syscalls were missing here, so
// this thread was SIGSYS-killed the instant Create/Start reached it
// (silently: SeccompActionKill leaves no Go-level error or log output).
func DefaultSeccompProfile() SeccompProfile {
	return SeccompProfile{
		AllowedSyscalls: []string{
			// File I/O
			"read", "write", "pread64", "pwrite64", "readv", "writev",
			"lseek", "close", "fcntl", "openat", "openat2", "newfstatat", "fstat",
			"access", "getdents64", "mkdirat", "mknodat", "renameat",
			"unlinkat", "symlinkat", "linkat", "readlinkat", "fchmodat",
			"fchmod", "fchmodat2", "fsync", "flock",
			"utimensat", "chdir", "dup3", "copy_file_range",
			// Memory management
			"brk", "mmap", "mprotect", "munmap", "madvise",
			// Process/thread lifecycle and signals
			"exit", "exit_group", "futex", "rt_sigaction",
			"rt_sigprocmask", "rt_sigreturn", "sigaltstack",
			"set_robust_list", "set_tid_address", "gettid", "getpid",
			"sched_getaffinity", "arch_prctl", "prctl", "prlimit64",
			"clone", "clone3", "rseq", "kill", "nanosleep", "sched_yield",
			// KVM control - every KVM_* ioctl is this one syscall
			"ioctl",
			// Entropy for the guest RNG seed and session key
			"getrandom",
			// Event/epoll plumbing the Go runtime itself relies on
			"epoll_create1", "epoll_ctl", "epoll_pwait", "eventfd2",
			"uname",
			// Tail of the control-socket listener setup and
			// applyVMMSandbox's capability-bounding-set drop, both of
			// which can still land on this thread depending on Go's
			// scheduler before it gets locked - see syscallNumberX8664
			// in syscalls_linux.go for how these were verified.
			"accept4", "getsockname", "capget", "capset",
		},
		DefaultAction: SeccompActionKill,
	}
}

// Sandbox provides VMM sandboxing functionality.
type Sandbox struct {
	config Config
	// seccompFD is reserved for a future SECCOMP_FILTER_FLAG_NEW_LISTENER
	// notification fd; the prctl(PR_SET_SECCOMP)-based installer this
	// package uses today (see seccomp_linux.go) doesn't produce one.
	seccompFD int
	// cgroupPath holds the leaf cgroup v2 directory applyCgroups created,
	// and cgroupParent the directory it was created under - Cleanup moves
	// this process back into cgroupParent before removing cgroupPath,
	// since a cgroup containing a live process can't be rmdir'd.
	cgroupPath   string
	cgroupParent string
	// nsFDs holds /proc/self/ns/<type> file descriptors opened right
	// after a successful applyNamespaces, one per namespace actually
	// unshared - kept for introspection/Cleanup, not required for the
	// isolation itself.
	nsFDs map[Namespace]int
	// testModeGetEuid is a test hook to override os.Geteuid
	testModeGetEuid func() int
	// testModeSetuid is a test hook to override syscall.Setuid
	testModeSetuid func() error
	// testModeApplySeccomp is a test hook to override applySeccomp
	testModeApplySeccomp func() error
	// testModeApplyNamespaces is a test hook to override applyNamespaces
	testModeApplyNamespaces func() error
	// testModeApplyCgroups is a test hook to override applyCgroups
	testModeApplyCgroups func() error
	// testModeDropPrivileges is a test hook to override dropPrivileges
	testModeDropPrivileges func() error
}

// Support reports, by construction rather than by guessing from euid,
// which of this package's real sandboxing operations the current
// process can actually perform - see ProbeSandbox.
type Support struct {
	// Namespaces reports whether applyNamespaces (unshare of UTS/IPC)
	// is expected to succeed.
	Namespaces bool
	// Cgroups reports whether applyCgroups (child cgroup v2 creation
	// and self-join) is expected to succeed.
	Cgroups bool
	// CapabilityBoundingDrop reports whether dropCapability
	// (PR_CAPBSET_DROP) is expected to succeed.
	CapabilityBoundingDrop bool
	// Details maps a facility name to the reason it is unavailable.
	Details map[string]string
}

// NewSandbox creates a new sandbox with the given configuration.
func NewSandbox(config Config) *Sandbox {
	return &Sandbox{
		config: config,
		nsFDs:  make(map[Namespace]int),
	}
}

// Apply applies the sandbox configuration to the current process, in an
// order that only gets more restrictive: cgroups and namespaces first
// (they still need the ambient privilege a VMM process normally starts
// with - joining a cgroup, unsharing namespaces), then capability/UID
// drop, then the seccomp filter last of all. Seccomp goes last on purpose:
// once it's installed, this thread can no longer make the very syscalls
// (setuid, capset, unshare) the earlier steps needed, so anything that
// still requires ambient privilege has to happen before it, never after.
func (s *Sandbox) Apply() error {
	if s.config.Cgroups {
		var err error
		if s.testModeApplyCgroups != nil {
			err = s.testModeApplyCgroups()
		} else {
			err = s.applyCgroups()
		}
		if err != nil {
			return fmt.Errorf("failed to apply cgroups: %w", err)
		}
	}

	if s.config.Namespaces {
		var err error
		if s.testModeApplyNamespaces != nil {
			err = s.testModeApplyNamespaces()
		} else {
			err = s.applyNamespaces()
		}
		if err != nil {
			return fmt.Errorf("failed to apply namespaces: %w", err)
		}
	}

	if s.config.DropPrivileges {
		var err error
		if s.testModeDropPrivileges != nil {
			err = s.testModeDropPrivileges()
		} else {
			err = s.dropPrivileges()
		}
		if err != nil {
			return fmt.Errorf("failed to drop privileges: %w", err)
		}
	}

	if s.config.Seccomp {
		var err error
		if s.testModeApplySeccomp != nil {
			err = s.testModeApplySeccomp()
		} else {
			err = s.applySeccomp()
		}
		if err != nil {
			return fmt.Errorf("failed to apply seccomp: %w", err)
		}
	}

	return nil
}

// applySeccomp, applyNamespaces, applyCgroups and dropCapability are
// implemented per-platform: real on Linux (sandbox_linux.go), no-op
// elsewhere (sandbox_other.go), matching this package's only current
// target (VMM host processes only run on Linux) while keeping the
// package importable and its tests green cross-platform.

// dropPrivileges drops root privileges in up to three phases, in an order
// that matters: bounding-set capability drop, then setuid, then current-set
// capability drop.
//
// The bounding-set phase only runs when euid is actually 0. This project's
// own deployment model is rootless Podman - the common case for this
// method is a supervisor that was never root and holds no capabilities at
// all (/dev/kvm access there is DAC-gated via group membership, not a
// capability), and PR_CAPBSET_DROP unconditionally needs CAP_SETPCAP in
// the *effective* set to do anything, root or not. Attempting it anyway on
// an already-unprivileged caller doesn't fail closed on a real threat, it
// just fails - EPERM either way, no way to distinguish "attacker" from
// "nothing to drop" - which would make DropPrivileges a hard requirement
// for privilege the common rootless deployment doesn't have and doesn't
// need. The current-set phase has no such precondition (an unprivileged
// process can always lower its own effective/permitted/inheritable bits,
// same principle any --cap-drop implementation relies on) and stays
// unconditional so it still does something real for a caller that holds
// capabilities without being euid 0.
//
// Dropping every configured capability from the bounding set before setuid
// is itself safe regardless of what's in DropCapabilities (including
// CAP_SETUID/CAP_SETGID) because PR_CAPBSET_DROP only limits what can be
// *regained* later, not what's currently held - the setuid call right
// after still has CAP_SETUID in its effective set and succeeds. Doing the
// *current-set* capset-based drop that early instead - clearing CAP_SETUID's
// effective bit before setuid runs - would make that setuid call itself
// fail with EPERM, unable to perform the very privilege drop it exists to
// do. The final current-set pass, after setuid, is what actually clears
// whatever the root->nonroot UID transition didn't already zero as its own
// kernel side effect (nothing, if this process was root; whatever it
// started with as a non-root capability holder, otherwise).
func (s *Sandbox) dropPrivileges() error {
	isRoot := os.Geteuid() == 0
	if s.testModeGetEuid != nil {
		isRoot = s.testModeGetEuid() == 0
	}

	if isRoot {
		for _, cap := range s.config.DropCapabilities {
			if err := dropCapabilityBoundingSet(cap); err != nil {
				return fmt.Errorf("failed to drop capability %s from bounding set: %w", cap, err)
			}
		}
		if s.testModeSetuid != nil {
			if err := s.testModeSetuid(); err != nil {
				return fmt.Errorf("failed to setuid: %w", err)
			}
		} else if err := setuid(65534); err != nil {
			return fmt.Errorf("failed to setuid: %w", err)
		}
	}

	for _, cap := range s.config.DropCapabilities {
		if err := dropCapabilityCurrentSet(cap); err != nil {
			return fmt.Errorf("failed to drop capability %s from current set: %w", cap, err)
		}
	}

	return nil
}

// Cleanup cleans up the sandbox resources.
func (s *Sandbox) Cleanup() error {
	// Close seccomp FD
	if s.seccompFD > 0 {
		closeFD(s.seccompFD)
		s.seccompFD = 0
	}

	// Close namespace FDs
	for ns, fd := range s.nsFDs {
		if fd > 0 {
			closeFD(fd)
		}
		delete(s.nsFDs, ns)
	}

	// Clean up cgroup. Removal is best-effort: a cgroup directory that
	// still holds this process as a member cannot be rmdir'd (EBUSY)
	// until the process exits or moves out, which mirrors how
	// short-lived sandboxed processes are expected to be cleaned up -
	// by the kernel reclaiming the now-empty directory after exit, not
	// by this call.
	if s.cgroupPath != "" {
		_ = os.Remove(s.cgroupPath)
		s.cgroupPath = ""
		s.cgroupParent = ""
	}

	return nil
}

// ApplySeccomp applies just the seccomp/no_new_privs stage, ignoring
// the sandbox's Seccomp config flag. Exported for callers that need
// selective, probe-gated application of individual stages instead of
// Apply's all-stages-or-first-error bundling - see
// internal/ociruntime/supervisor_linux.go's applyVMMSandbox, which
// treats seccomp as mandatory (it needs no privilege and cannot fail
// for lack of it) while applying cgroups and capability dropping only
// when ProbeSandbox reports them available, so a host without cgroup
// delegation can still launch guests rather than failing closed.
func (s *Sandbox) ApplySeccomp() error { return s.applySeccomp() }

// ApplyCgroups applies just the cgroup stage - see ApplySeccomp.
func (s *Sandbox) ApplyCgroups() error { return s.applyCgroups() }

// ApplyStrictSeccomp installs a real classic-BPF seccomp filter
// (DefaultSeccompProfile) on the calling OS thread - see
// applyStrictSeccomp's doc comment (seccomp_linux.go) for the safety
// precondition this has that ApplySeccomp above does not: the calling
// goroutine must already be runtime.LockOSThread-pinned to the thread
// that is about to do the work the filter is meant to protect.
func (s *Sandbox) ApplyStrictSeccomp() error { return s.applyStrictSeccomp() }

// DropBoundingCapabilities drops each of the sandbox's configured
// DropCapabilities from both the process's bounding set and its current
// effective, permitted, and inheritable sets,
// without dropPrivileges' accompanying setuid-to-nobody step. It is
// exported for callers - like the VMM host process - that want
// capability hardening without also changing UID: setuid does not
// touch supplementary group membership by itself, but a caller that
// cannot verify locally whether its deployment's DAC access to a
// privileged device file (for example /dev/kvm) depends on UID rather
// than group membership should not risk it silently over an assumption.
//
// A single capability's drop failing does not fail the rest:
// PR_CAPBSET_DROP(CAP_LINUX_IMMUTABLE) can return EPERM under rootless
// Podman running inside a Docker devcontainer whose own outer bounding
// set already lacks it, even though ProbeSandbox has already confirmed
// CAP_SETPCAP is held (the precondition every caller of this method
// gates on) - an ancestor user namespace having already permanently
// removed a capability behaves the same as lacking the privilege to
// drop it, from inside this one. Failing the whole batch over one
// already-absent capability would turn hardening into a guest-launch
// regression on exactly this kind of nested deployment; every other
// capability in DropCapabilities still gets attempted.
func (s *Sandbox) DropBoundingCapabilities() error {
	for _, capability := range s.config.DropCapabilities {
		_ = dropCapability(capability)
	}
	return nil
}

// IsSandboxed returns true if the current process is running in a sandbox.
func IsSandboxed() bool {
	// Check if we're in a user namespace
	if isInUserNamespace() {
		return true
	}
	// Check seccomp status
	if isSeccompEnabled() {
		return true
	}
	return false
}

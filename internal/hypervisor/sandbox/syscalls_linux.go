//go:build linux && amd64

package sandbox

import "syscall"

// syscallNumberX8664 maps the syscall names DefaultSeccompProfile (and any
// caller-supplied SeccompProfile) may list to their linux/amd64 syscall
// numbers, resolved wherever possible through the syscall package's own
// SYS_* constants rather than transcribing numbers by hand - the same
// approach internal/hypervisor/kvm already uses for KVM ioctl request codes.
//
// A handful of syscalls this package's own real allow-list needs
// (getrandom, copy_file_range, rseq, clone3, openat2, fchmodat2) were
// added to the x86_64 ABI after go1.10's syscall.SYS_* table was
// generated and have no stdlib constant; those six are the raw,
// long-stable numbers from arch/x86/entry/syscalls/syscall_64.tbl,
// called out individually below.
var syscallNumberX8664 = map[string]uint32{
	// File I/O. renameat/mknodat/symlinkat/linkat/readlinkat/fchmodat/
	// utimensat/getdents64 back internal/rootfs.Convert's whiteout,
	// symlink, hardlink and FIFO handling (os.Root.Link -> linkat,
	// syscall.Mkfifo -> Mknodat, os.Rename -> Renameat); copy_file_range
	// backs the initramfs writer's file copies.
	"read":       uint32(syscall.SYS_READ),
	"write":      uint32(syscall.SYS_WRITE),
	"pread64":    uint32(syscall.SYS_PREAD64),
	"pwrite64":   uint32(syscall.SYS_PWRITE64),
	"readv":      uint32(syscall.SYS_READV),
	"writev":     uint32(syscall.SYS_WRITEV),
	"lseek":      uint32(syscall.SYS_LSEEK),
	"close":      uint32(syscall.SYS_CLOSE),
	"fcntl":      uint32(syscall.SYS_FCNTL),
	"openat":     uint32(syscall.SYS_OPENAT),
	"openat2":    437, // arch/x86/entry/syscalls/syscall_64.tbl; os.Root's confined-open path
	"newfstatat": uint32(syscall.SYS_NEWFSTATAT),
	"fstat":      uint32(syscall.SYS_FSTAT),
	"access":     uint32(syscall.SYS_ACCESS),
	"getdents64": uint32(syscall.SYS_GETDENTS64),
	"mkdirat":    uint32(syscall.SYS_MKDIRAT),
	"mknodat":    uint32(syscall.SYS_MKNODAT),
	"renameat":   uint32(syscall.SYS_RENAMEAT),
	"unlinkat":   uint32(syscall.SYS_UNLINKAT),
	"symlinkat":  uint32(syscall.SYS_SYMLINKAT),
	"linkat":     uint32(syscall.SYS_LINKAT),
	"readlinkat": uint32(syscall.SYS_READLINKAT),
	"fchmodat":   uint32(syscall.SYS_FCHMODAT),
	"fchmodat2":  452, // arch/x86/entry/syscalls/syscall_64.tbl; os.Root's confined, symlink-safe chmod
	"utimensat":  uint32(syscall.SYS_UTIMENSAT),
	"chdir":      uint32(syscall.SYS_CHDIR),
	"dup3":       uint32(syscall.SYS_DUP3),
	"fchmod":     uint32(syscall.SYS_FCHMOD),
	"fsync":      uint32(syscall.SYS_FSYNC),
	"flock":      uint32(syscall.SYS_FLOCK),

	// Memory management: guest memory / kvm_run mmap, Go heap growth.
	"brk":      uint32(syscall.SYS_BRK),
	"mmap":     uint32(syscall.SYS_MMAP),
	"mprotect": uint32(syscall.SYS_MPROTECT),
	"munmap":   uint32(syscall.SYS_MUNMAP),
	"madvise":  uint32(syscall.SYS_MADVISE),

	// Process/thread lifecycle and signals. execve is deliberately not
	// in this table: nothing on the confined thread ever execs after
	// the filter is installed (LaunchSupervisor's exec.Command runs in
	// the parent, not here), so leaving it out is a real hardening
	// property, not an oversight.
	"exit":              uint32(syscall.SYS_EXIT),
	"exit_group":        uint32(syscall.SYS_EXIT_GROUP),
	"futex":             uint32(syscall.SYS_FUTEX),
	"rt_sigaction":      uint32(syscall.SYS_RT_SIGACTION),
	"rt_sigprocmask":    uint32(syscall.SYS_RT_SIGPROCMASK),
	"rt_sigreturn":      uint32(syscall.SYS_RT_SIGRETURN),
	"sigaltstack":       uint32(syscall.SYS_SIGALTSTACK),
	"set_robust_list":   uint32(syscall.SYS_SET_ROBUST_LIST),
	"set_tid_address":   uint32(syscall.SYS_SET_TID_ADDRESS),
	"gettid":            uint32(syscall.SYS_GETTID),
	"getpid":            uint32(syscall.SYS_GETPID),
	"sched_getaffinity": uint32(syscall.SYS_SCHED_GETAFFINITY),
	"arch_prctl":        uint32(syscall.SYS_ARCH_PRCTL),
	"prctl":             uint32(syscall.SYS_PRCTL),
	"prlimit64":         uint32(syscall.SYS_PRLIMIT64),
	// runtime.newosproc (sys_linux_amd64.s) creates every new OS thread
	// via plain clone(2), not clone3 - GC workers, sysmon, and blocked-
	// syscall handoff can all trigger this on the confined thread's
	// process at any time after the filter is installed, so leaving it
	// out kills the whole process (SeccompActionKill has no exception
	// for defers) the moment the runtime needs one more thread than it
	// already has.
	"clone":  uint32(syscall.SYS_CLONE),
	"clone3": 435, // arch/x86/entry/syscalls/syscall_64.tbl; post-dates syscall.SYS_*

	// KVM control: every KVM_* ioctl (KVM_RUN, KVM_IRQ_LINE, ...) is
	// this one syscall with a request-code argument classic BPF cannot
	// itself narrow further - internal/hypervisor/kvm is the only
	// caller reachable from this confined thread.
	"ioctl": uint32(syscall.SYS_IOCTL),

	// Entropy for the per-boot guest RNG seed and guest session key.
	"getrandom": 318, // arch/x86/entry/syscalls/syscall_64.tbl

	// Event/epoll plumbing the Go network poller and runtime timers use.
	"epoll_create1": uint32(syscall.SYS_EPOLL_CREATE1),
	"epoll_ctl":     uint32(syscall.SYS_EPOLL_CTL),
	"epoll_pwait":   uint32(syscall.SYS_EPOLL_PWAIT),
	"eventfd2":      uint32(syscall.SYS_EVENTFD2),

	"uname":           uint32(syscall.SYS_UNAME),
	"copy_file_range": 326, // arch/x86/entry/syscalls/syscall_64.tbl
	"rseq":            334, // arch/x86/entry/syscalls/syscall_64.tbl

	// Needed by the create -> ServeSupervisor -> buildGuestInitramfs ->
	// RunLinuxWithOptions call path: accept4/getsockname are the tail of
	// the control-socket listener setup that can still land on this
	// thread depending on Go's scheduler; capget/capset back
	// applyVMMSandbox's capability-bounding-set drop landing on the
	// same OS thread before it gets locked; kill/nanosleep/sched_yield
	// are Go runtime scheduling/timer primitives that can land on this
	// thread.
	"accept4":     uint32(syscall.SYS_ACCEPT4),
	"getsockname": uint32(syscall.SYS_GETSOCKNAME),
	"capget":      uint32(syscall.SYS_CAPGET),
	"capset":      uint32(syscall.SYS_CAPSET),
	"kill":        uint32(syscall.SYS_KILL),
	"nanosleep":   uint32(syscall.SYS_NANOSLEEP),
	"sched_yield": uint32(syscall.SYS_SCHED_YIELD),
	// tgkill backs Go's asynchronous goroutine preemption (runtime
	// sysmon's SIGURG signal, used since Go 1.14 to interrupt a
	// goroutine that has run too long without reaching a safepoint -
	// e.g. a tight byte-copying loop during a large kernel/initramfs
	// read). It can fire at any point once this thread has run long
	// enough, entirely independent of which code is executing, which is
	// why its absence here reproduced as an intermittent, unexplained
	// SIGSYS process kill rather than a deterministic failure at one
	// call site - verified against a real kernel boot by bisecting a
	// strace of the killed supervisor down to the exact
	// tgkill(pid, tid, SIGURG) call the kernel's SECCOMP_RET_KILL_PROCESS
	// default action fired on.
	"tgkill": uint32(syscall.SYS_TGKILL),
}

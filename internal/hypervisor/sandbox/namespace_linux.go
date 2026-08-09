//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
)

// defaultNamespaces is what applyNamespaces unshares when
// Config.NamespaceList is left unset - see that field's doc comment in
// sandbox.go for why mount/PID/user aren't in it.
var defaultNamespaces = []Namespace{NamespaceNET, NamespaceUTS, NamespaceIPC}

// namespaceCloneFlag maps the namespace types this package actually
// supports unsharing mid-process to their CLONE_NEW* flag. Mount, PID and
// user namespaces are deliberately absent: see NamespaceList's doc comment.
func namespaceCloneFlag(ns Namespace) (int, bool) {
	switch ns {
	case NamespaceNET:
		return syscall.CLONE_NEWNET, true
	case NamespaceUTS:
		return syscall.CLONE_NEWUTS, true
	case NamespaceIPC:
		return syscall.CLONE_NEWIPC, true
	default:
		return 0, false
	}
}

// applyNamespaces unshares s.config's selected namespaces for the calling
// OS thread via syscall.Unshare - the stdlib wrapper around unshare(2),
// which for these namespace types takes effect on the calling thread alone
// rather than the whole process (unlike PID namespaces, which only affect
// children created after the call). That's exactly the scope this package
// relies on: the caller is expected to have already locked the goroutine
// calling Apply to its OS thread, matching internal/ociruntime's AppArmor
// confinement precedent, so "this thread" is the one, unshared-for-life
// thread that goes on to build the guest initramfs and run KVM_RUN.
//
// It needs CAP_SYS_ADMIN (or a fresh, paired CLONE_NEWUSER, which this
// package does not attempt - see NamespaceList) in whatever namespace it's
// starting from; a real deployment runs the VMM supervisor with that
// privilege already, the same way it needs privilege to open /dev/kvm.
func (s *Sandbox) applyNamespaces() error {
	list := s.config.NamespaceList
	if len(list) == 0 {
		list = defaultNamespaces
	}
	flags := 0
	for _, ns := range list {
		flag, ok := namespaceCloneFlag(ns)
		if !ok {
			return fmt.Errorf("sandbox: namespace %q is not supported for in-process unshare", ns)
		}
		flags |= flag
	}
	if flags == 0 {
		return nil
	}
	if err := syscall.Unshare(flags); err != nil {
		return fmt.Errorf("unshare: %w", err)
	}
	for _, ns := range list {
		fd, err := os.Open("/proc/self/ns/" + string(ns))
		if err != nil {
			// The unshare above already succeeded; failing to keep a
			// reference fd for introspection doesn't undo it and isn't
			// worth failing Apply over.
			continue
		}
		// *os.File carries a GC finalizer that closes its descriptor;
		// nsFDs keeps only the raw int and Cleanup closes it directly
		// (closeFD), so the finalizer needs disarming here or it can
		// close this same fd out from under Cleanup once fd is
		// unreachable.
		runtime.SetFinalizer(fd, nil)
		s.nsFDs[ns] = int(fd.Fd())
	}
	return nil
}

// isInUserNamespace reports whether the calling process is in a non-default
// user namespace, by checking whether /proc/self/uid_map differs from the
// kernel's initial-namespace identity mapping ("0 0 4294967295"): that file
// always exists on Linux 3.8+, in the root namespace as much as any other,
// so its mere presence (the previous version of this check) doesn't
// distinguish anything.
func isInUserNamespace() bool {
	data, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return false
	}
	return string(bytesTrimSpace(data)) != "0 0 4294967295"
}

func bytesTrimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\n') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t' || b[len(b)-1] == '\n') {
		b = b[:len(b)-1]
	}
	return b
}

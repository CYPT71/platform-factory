//go:build linux

package sandbox

import (
	"fmt"
	"syscall"
	"unsafe"
)

// capabilityBits maps every name in AllCapabilities to its Linux capability
// bit number (linux/capability.h CAP_*). These are stable, published ABI
// constants - the list and its order already matched CAP_CHOWN=0 through
// CAP_CHECKPOINT_RESTORE=40 before this file existed, which is what let it
// double as this table's construction below.
var capabilityBits = func() map[string]uint {
	bits := make(map[string]uint, len(AllCapabilities))
	for i, name := range AllCapabilities {
		bits[name] = uint(i)
	}
	return bits
}()

const (
	prCapbsetDrop = 24

	sysCapget = 125
	sysCapset = 126

	// linux/capability.h: capabilities beyond the first 32 (everything
	// from CAP_MAC_OVERRIDE=32 up) require the version-3 header and two
	// data words instead of one.
	linuxCapabilityVersion3 = 0x20080522
)

// capUserHeader mirrors struct __user_cap_header_struct.
type capUserHeader struct {
	version uint32
	pid     int32
}

// capUserData mirrors struct __user_cap_data_struct; capset/capget with the
// version-3 header take an array of two of these (low 32 caps, then the
// next 32).
type capUserData struct {
	effective   uint32
	permitted   uint32
	inheritable uint32
}

// dropCapabilityBoundingSet removes cap from the calling thread's bounding
// set via PR_CAPBSET_DROP, so it can never be regained later via exec or
// another set*id call. It leaves the *current* effective/permitted set
// untouched - deliberately: dropPrivileges calls this for every configured
// capability, including CAP_SETUID/CAP_SETGID, before its own setuid call,
// and setuid still needs CAP_SETUID in the effective set to succeed at
// that point. dropCapabilityCurrentSet is the other half.
func dropCapabilityBoundingSet(cap string) error {
	bit, ok := capabilityBits[cap]
	if !ok {
		return fmt.Errorf("sandbox: unknown capability %q", cap)
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_PRCTL, prCapbsetDrop, uintptr(bit), 0); errno != 0 && errno != syscall.EINVAL && errno != syscall.EPERM {
		// EINVAL means the running kernel doesn't know about a
		// capability this new (AllCapabilities includes CAP_BPF and
		// CAP_CHECKPOINT_RESTORE, both from Linux 5.8) - nothing to
		// drop from a bounding set that was never able to hold it.
		//
		// EPERM here does not necessarily mean this call lacks
		// CAP_SETPCAP - callers already gate this on that (see
		// dropPrivileges' isRoot check and DropBoundingCapabilities'
		// callers, both of which only reach this after confirming
		// CAP_SETPCAP is held). Under rootless Podman - which maps the
		// invoking user to UID 0 and full effective capabilities
		// *inside its own user namespace*, so both of those checks pass
		// - PR_CAPBSET_DROP still returns EPERM for a capability the
		// *outer* container runtime already permanently removed from
		// this process's ancestry (e.g. CAP_LINUX_IMMUTABLE, CAP_CHOWN
		// under a typical devcontainer). A capability an ancestor
		// namespace already dropped can never come back, so there is
		// nothing left for this call to do - tolerating EPERM here is a
		// no-op, not a silently-ignored real failure.
		return fmt.Errorf("prctl(PR_CAPBSET_DROP, %s): %w", cap, errno)
	}
	return nil
}

// dropCapabilityCurrentSet clears cap's bit in the calling thread's actual
// effective/permitted/inheritable sets via capget/capset. Lowering your own
// capability sets never needs any privilege beyond having the bit set in
// the first place, so this is safe to call at any point - including after
// dropPrivileges' setuid, where a root->nonroot UID transition has
// generally already zeroed these sets as a kernel side effect anyway (this
// call is then a documented no-op, not a wasted one: a caller that never
// held root to begin with still needs it to actually drop anything).
func dropCapabilityCurrentSet(cap string) error {
	bit, ok := capabilityBits[cap]
	if !ok {
		return fmt.Errorf("sandbox: unknown capability %q", cap)
	}
	header := capUserHeader{version: linuxCapabilityVersion3}
	var data [2]capUserData
	if _, _, errno := syscall.Syscall(uintptr(sysCapget), uintptr(unsafe.Pointer(&header)), uintptr(unsafe.Pointer(&data[0])), 0); errno != 0 {
		return fmt.Errorf("capget: %w", errno)
	}
	word, shift := bit/32, bit%32
	mask := ^(uint32(1) << shift)
	data[word].effective &= mask
	data[word].permitted &= mask
	data[word].inheritable &= mask

	// capget fills in header.pid on return; capset requires it to name
	// the calling process again, not whatever capget happened to report.
	header.pid = 0
	if _, _, errno := syscall.Syscall(uintptr(sysCapset), uintptr(unsafe.Pointer(&header)), uintptr(unsafe.Pointer(&data[0])), 0); errno != 0 {
		return fmt.Errorf("capset(%s): %w", cap, errno)
	}
	return nil
}

// dropCapability is dropCapabilityBoundingSet followed by
// dropCapabilityCurrentSet: the simple "just drop this one capability right
// now" entry point for a caller that (unlike dropPrivileges) isn't about to
// change UID and doesn't need the two halves sequenced around that call.
func dropCapability(cap string) error {
	if err := dropCapabilityBoundingSet(cap); err != nil {
		return err
	}
	return dropCapabilityCurrentSet(cap)
}

//go:build linux

package kvm

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// This file creates the host-side TAP descriptor a virtio-net device (see
// linux_virtio_net.go) reads and writes raw Ethernet frames on. It is
// Linux-only but deliberately not amd64-only like the rest of this
// package's virtio files: opening /dev/net/tun has nothing amd64-specific
// about it, unlike the x86 Linux boot protocol and KVM ioctls the
// linux&&amd64 files are tied to.
const (
	tunDevicePath = "/dev/net/tun"

	// linux/if_tun.h. IFF_TAP selects a TAP (link-layer/Ethernet) device
	// rather than IFF_TUN (network-layer/IP); IFF_NO_PI removes the
	// 4-byte "packet information" header the kernel would otherwise
	// prepend to every frame read from the descriptor - virtio-net's own
	// framing (linux_virtio_net.go's virtioNetHeaderSize) is the only
	// per-frame header this VMM wants to deal with.
	iffTap  = uint16(0x0002)
	iffNoPI = uint16(0x1000)

	// TUNSETIFF = _IOW('T', 202, int) under the standard Linux ioctl
	// encoding ((1<<30)|(4<<16)|('T'<<8)|202) = 0x400454ca - the same
	// value every TAP-creating program (iproute2, QEMU, every container
	// runtime's CNI plugin) uses; not specific to this VMM.
	tunSetIff = uintptr(0x400454ca)

	// IFNAMSIZ (linux/if.h): the fixed buffer size the kernel expects an
	// interface name in, NUL-padded, including the terminator.
	ifNameSize = 16

	// SIOCSIFADDR/SIOCSIFNETMASK/SIOCGIFFLAGS/SIOCSIFFLAGS (linux/sockios.h):
	// the same ioctls `ip addr add`/`ip link set up` issue against a
	// generic AF_INET control socket - never against the TAP character
	// device fd itself, which only understands the TUNSETIFF family above.
	siocSIFAddr    = uintptr(0x8916)
	siocSIFNetmask = uintptr(0x891c)
	siocGIFFlags   = uintptr(0x8913)
	siocSIFFlags   = uintptr(0x8914)

	ifFlagUp = uint16(0x1) // IFF_UP (linux/if.h)
)

// ifReqName mirrors just the ifr_name/ifr_flags prefix of struct ifreq
// (linux/if.h) that TUNSETIFF actually reads, padded out to the real
// struct's full 32-byte size on Linux/amd64 (16-byte name + a 16-byte
// union, of which only the first 2 bytes - ifr_flags - are ever set here).
// The kernel's own copy_from_user uses its compiled sizeof(struct ifreq),
// not whatever Go declares, so this only needs to be large enough, not
// byte-identical beyond the two fields TUNSETIFF documents using.
type ifReqName struct {
	Name  [ifNameSize]byte
	Flags uint16
	_     [14]byte
}

// ifReqAddr mirrors the ifr_name/ifr_addr prefix of struct ifreq that
// SIOCSIFADDR/SIOCSIFNETMASK read: a 16-byte name followed directly by a
// struct sockaddr_in (2-byte family, 2-byte port - unused for an address
// assignment, always zero - 4-byte IPv4 address, 8 bytes of padding),
// which is exactly struct ifreq's union size on Linux/amd64, the same
// contract ifReqName documents for TUNSETIFF.
type ifReqAddr struct {
	Name   [ifNameSize]byte
	Family uint16
	Port   uint16
	Addr   [4]byte
	_      [8]byte
}

// OpenTAP opens (or, if name already names a persistent TAP interface
// owned by this user, attaches to) a TAP network interface and returns an
// IFF_TAP|IFF_NO_PI descriptor ready for NetworkDeviceOptions.TAP, along
// with the interface name the kernel actually assigned (relevant when name
// is empty: the kernel picks one, conventionally "tap0", "tap1", ...).
//
// This needs CAP_NET_ADMIN in the caller's network namespace - see
// ProbeTAPSupport for a way to check that without attempting the creation
// itself, the same probe-before-attempt pattern
// internal/hypervisor/sandbox uses for its own privileged operations.
// Failure here is not gated or made optional by this function: a caller
// that wants "skip networking rather than fail guest launch" behavior
// checks ProbeTAPSupport first and never calls OpenTAP at all when it
// returns false, exactly how internal/ociruntime's applyVMMSandbox already
// treats sandbox.ProbeSandbox for the same reason.
func OpenTAP(name string) (tap *os.File, actualName string, err error) {
	if len(name) >= ifNameSize {
		return nil, "", fmt.Errorf("vmm: tap: interface name %q is too long (IFNAMSIZ is %d bytes including the NUL)", name, ifNameSize)
	}
	fd, err := syscall.Open(tunDevicePath, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", fmt.Errorf("vmm: tap: open %s: %w", tunDevicePath, err)
	}
	var req ifReqName
	copy(req.Name[:], name)
	req.Flags = iffTap | iffNoPI
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tunSetIff, uintptr(unsafe.Pointer(&req))); errno != 0 {
		_ = syscall.Close(fd)
		return nil, "", fmt.Errorf("vmm: tap: TUNSETIFF: %w", errno)
	}
	// See testTAPPair's doc comment (linux_virtio_net_test.go) for why
	// this matters: os.NewFile only integrates a descriptor with the Go
	// runtime's poller - what makes Close() from another goroutine
	// actually interrupt a Read() blocked on this same fd, which
	// linux_virtio_net.go's stop() depends on - if the descriptor is
	// already non-blocking before NewFile wraps it.
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, "", fmt.Errorf("vmm: tap: set non-blocking: %w", err)
	}
	actualName = string(bytes.TrimRight(req.Name[:], "\x00"))
	return os.NewFile(uintptr(fd), actualName), actualName, nil
}

// AssignTAPAddress configures a TAP interface's own host-side IPv4 address
// (SIOCSIFADDR/SIOCSIFNETMASK) and brings it up (SIOCSIFFLAGS |= IFF_UP) -
// the two steps `ip addr add <cidr> dev <name>` and `ip link set <name> up`
// perform, done here directly against a throwaway AF_INET control socket
// rather than shelling out, matching this file's own no-external-tool
// contract. name is the interface name OpenTAP returned; cidr is the
// host-side address to assign, e.g. "169.254.100.1/30".
//
// This does not by itself give the guest network access beyond this one
// point-to-point link: it only makes the host side of the TAP interface
// reachable, which is what a caller relaying host TCP connections onto a
// fixed guest IP (see internal/microvm/forward) needs and nothing more -
// no routing, no NAT, no DHCP server are set up here.
func AssignTAPAddress(name, cidr string) error {
	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("vmm: tap: parse %q: %w", cidr, err)
	}
	ipv4 := ip.To4()
	if ipv4 == nil {
		return fmt.Errorf("vmm: tap: %q is not an IPv4 address", cidr)
	}
	if len(name) >= ifNameSize {
		return fmt.Errorf("vmm: tap: interface name %q is too long (IFNAMSIZ is %d bytes including the NUL)", name, ifNameSize)
	}

	control, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("vmm: tap: open control socket: %w", err)
	}
	defer syscall.Close(control)

	if err := ifreqSetAddr(control, name, siocSIFAddr, ipv4); err != nil {
		return fmt.Errorf("vmm: tap: SIOCSIFADDR: %w", err)
	}
	if err := ifreqSetAddr(control, name, siocSIFNetmask, net.IP(network.Mask).To4()); err != nil {
		return fmt.Errorf("vmm: tap: SIOCSIFNETMASK: %w", err)
	}

	var flagsReq ifReqName
	copy(flagsReq.Name[:], name)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(control), siocGIFFlags, uintptr(unsafe.Pointer(&flagsReq))); errno != 0 {
		return fmt.Errorf("vmm: tap: SIOCGIFFLAGS: %w", errno)
	}
	flagsReq.Flags |= ifFlagUp
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(control), siocSIFFlags, uintptr(unsafe.Pointer(&flagsReq))); errno != 0 {
		return fmt.Errorf("vmm: tap: SIOCSIFFLAGS: %w", errno)
	}
	return nil
}

func ifreqSetAddr(control int, name string, request uintptr, addr net.IP) error {
	var req ifReqAddr
	copy(req.Name[:], name)
	req.Family = syscall.AF_INET
	copy(req.Addr[:], addr)
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(control), request, uintptr(unsafe.Pointer(&req)))
	if errno != 0 {
		return errno
	}
	return nil
}

// TAPSupport reports whether OpenTAP is expected to succeed, and why not
// when it isn't - the same "report by construction, don't guess from
// euid" contract internal/hypervisor/sandbox.ProbeSandbox already
// documents for its own privileged operations.
type TAPSupport struct {
	Available bool
	Reason    string
}

// ProbeTAPSupport checks CAP_NET_ADMIN in the calling process's own
// effective capability set, read from /proc/self/status - the same
// primitive (and the same reasoning for using it instead of attempting
// the real operation) as internal/hypervisor/sandbox's hasCapability: a
// real TUNSETIFF attempt is not idempotent enough to use as a probe (a
// persistent TAP interface it half-creates before failing on a later
// step would outlive the probe), and this never mutates process or
// network state.
func ProbeTAPSupport() TAPSupport {
	const capNetAdmin = 12 // linux/capability.h CAP_NET_ADMIN
	if !hasEffectiveCapability(capNetAdmin) {
		return TAPSupport{Reason: "CAP_NET_ADMIN not held; TUNSETIFF would fail"}
	}
	return TAPSupport{Available: true}
}

// hasEffectiveCapability reports whether the calling process currently
// holds capability bit in its effective set, per /proc/self/status's
// CapEff bitmask.
func hasEffectiveCapability(bit uint) bool {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		hex, ok := strings.CutPrefix(line, "CapEff:")
		if !ok {
			continue
		}
		mask, err := strconv.ParseUint(strings.TrimSpace(hex), 16, 64)
		if err != nil {
			return false
		}
		return mask&(1<<bit) != 0
	}
	return false
}

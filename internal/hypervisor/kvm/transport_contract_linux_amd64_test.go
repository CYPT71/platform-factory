//go:build linux && amd64

package kvm

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestVirtioTransportRoutesOnlyItsOwnMMIOWindow(t *testing.T) {
	first := newVirtioMMIODevice("first", 1, virtioMMIODeviceBaseAddress(0), 5, 1, 0, nil)
	second := newVirtioMMIODevice("second", 2, virtioMMIODeviceBaseAddress(1), 6, 1, 0, nil)
	if !strings.Contains(first.virtioMMIOCommandLineParam(), "virtio_mmio.device=") {
		t.Fatal("missing guest discovery parameter")
	}
	for _, device := range []*virtioMMIODevice{first, second} {
		buffer := make([]byte, 4)
		handled, err := handleLinuxVirtioMMIO([]*virtioMMIODevice{first, second}, device.base, false, buffer)
		if err != nil || !handled || binary.LittleEndian.Uint32(buffer) != 0x74726976 {
			t.Fatalf("device magic: %x %v %v", buffer, handled, err)
		}
	}
	for _, address := range []uint64{first.base - 1, second.base + virtioMMIOWindowSize} {
		if handled, err := handleLinuxVirtioMMIO([]*virtioMMIODevice{first, second}, address, false, make([]byte, 4)); err != nil || handled {
			t.Fatal("unclaimed MMIO address was serviced")
		}
	}
	for _, bad := range []struct{ name, cidr string }{{"x", "bad"}, {"x", "::1/128"}, {strings.Repeat("x", ifNameSize), "127.0.0.1/8"}} {
		if err := AssignTAPAddress(bad.name, bad.cidr); err == nil {
			t.Fatalf("invalid TAP request accepted: %+v", bad)
		}
	}
	if _, _, err := OpenTAP(strings.Repeat("x", ifNameSize)); err == nil {
		t.Fatal("oversized TAP name accepted")
	}
	if _, err := guestRange(make([]byte, 8), ^uint64(0)-1, 4); err == nil {
		t.Fatal("guest memory overflow accepted")
	}
	if got, err := guestRange(make([]byte, 8), 100, 0); err != nil || len(got) != 0 {
		t.Fatal("empty guest range touched memory")
	}
}

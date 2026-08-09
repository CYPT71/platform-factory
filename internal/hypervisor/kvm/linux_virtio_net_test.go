//go:build linux && amd64

package kvm

import (
	"encoding/binary"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

// testTAPPair returns two connected, message-boundary-preserving
// descriptors standing in for a TAP device and its "other end" (the host
// network stack, in a real deployment): a SOCK_DGRAM Unix socketpair reads
// back exactly what was written in one call, the same framing guarantee a
// real /dev/net/tun character device gives - and unlike opening a real TAP
// device, this needs no CAP_NET_ADMIN, so it runs in any test environment.
func testTAPPair(t *testing.T) (device *os.File, peer *os.File) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	// os.NewFile only integrates a descriptor with the Go runtime's poller
	// - the thing that makes Close() from another goroutine actually
	// interrupt a Read() blocked on this same fd, which stop() depends on
	// - if the descriptor is already non-blocking at the moment NewFile
	// wraps it. syscall.Socketpair returns blocking descriptors by
	// default; a real TAP fd from os.OpenFile("/dev/net/tun", ...)
	// doesn't have this problem (OpenFile always arranges it for a
	// character device), but this hand-rolled substitute does, and
	// without this line stop() hangs forever waiting for a Read() that
	// Close() never actually woke up.
	for _, fd := range fds {
		if err := syscall.SetNonblock(fd, true); err != nil {
			t.Fatalf("SetNonblock: %v", err)
		}
	}
	return os.NewFile(uintptr(fds[0]), "tap-device-side"), os.NewFile(uintptr(fds[1]), "tap-peer-side")
}

func TestVirtioNetTransmitReachesTAP(t *testing.T) {
	tapDevice, peer := testTAPPair(t)
	defer peer.Close()
	mac := net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	device, stop, err := newVirtioNetMMIODevice(NetworkDeviceOptions{TAP: tapDevice, MAC: mac}, virtioMMIODeviceBaseAddress(0), virtioFirstDeviceIRQ)
	if err != nil {
		t.Fatalf("newVirtioNetMMIODevice: %v", err)
	}
	defer stop()
	g := newTestVirtioMem(len(device.queues))
	device.guestMemory = g.mem
	device.raiseIRQ = func() {}
	driveToDriverOK(t, device, g)
	if deviceID := reg32(t, device, virtioRegDeviceID, false, 0); deviceID != virtioDeviceIDNet {
		t.Fatalf("DeviceID = %d, want %d", deviceID, virtioDeviceIDNet)
	}

	// TX is queue 1: a driver-readable virtio_net_hdr (all zero, no
	// offloads negotiated) followed by the raw Ethernet frame.
	frame := append([]byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x00}, []byte("hello from the guest")...)
	headerAddr := g.alloc(virtioNetHeaderSize)
	clear(g.mem[headerAddr : headerAddr+virtioNetHeaderSize])
	dataAddr := g.alloc(len(frame))
	copy(g.mem[dataAddr:], frame)
	g.writeDesc(int(virtioNetQueueTX), 0, headerAddr, virtioNetHeaderSize, virtqDescFlagNext, 1)
	g.writeDesc(int(virtioNetQueueTX), 1, dataAddr, uint32(len(frame)), 0, 0)
	g.publish(int(virtioNetQueueTX), 0)

	reg32(t, device, virtioRegQueueSel, true, virtioNetQueueTX)
	reg32(t, device, virtioRegQueueNotify, true, virtioNetQueueTX)

	if err := peer.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 2048)
	n, err := peer.Read(got)
	if err != nil {
		t.Fatalf("reading the frame this device should have written to the TAP peer: %v", err)
	}
	if string(got[:n]) != string(frame) {
		t.Fatalf("TAP received %q, want %q", got[:n], frame)
	}
}

func TestVirtioNetReceiveDeliversFromTAP(t *testing.T) {
	tapDevice, peer := testTAPPair(t)
	defer peer.Close()
	mac := net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x02}
	device, stop, err := newVirtioNetMMIODevice(NetworkDeviceOptions{TAP: tapDevice, MAC: mac}, virtioMMIODeviceBaseAddress(0), virtioFirstDeviceIRQ)
	if err != nil {
		t.Fatalf("newVirtioNetMMIODevice: %v", err)
	}
	defer stop()
	g := newTestVirtioMem(len(device.queues))
	device.guestMemory = g.mem
	irqCh := make(chan struct{}, 8)
	device.raiseIRQ = func() {
		select {
		case irqCh <- struct{}{}:
		default:
		}
	}
	driveToDriverOK(t, device, g)

	// Post one empty, device-writable RX buffer on queue 0 - what a real
	// driver does ahead of time so the device has somewhere to deliver an
	// inbound frame the moment one arrives.
	rxHeaderAddr := g.alloc(virtioNetHeaderSize)
	rxDataAddr := g.alloc(2048)
	g.writeDesc(int(virtioNetQueueRX), 0, rxHeaderAddr, virtioNetHeaderSize, virtqDescFlagNext|virtqDescFlagWrite, 1)
	g.writeDesc(int(virtioNetQueueRX), 1, rxDataAddr, 2048, virtqDescFlagWrite, 0)
	slot := g.publish(int(virtioNetQueueRX), 0)
	reg32(t, device, virtioRegQueueSel, true, virtioNetQueueRX)
	reg32(t, device, virtioRegQueueNotify, true, virtioNetQueueRX)

	inboundFrame := append([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x02, 0x00, 0x00, 0x00, 0x00, 0x02, 0x08, 0x00}, []byte("hello from the host")...)
	if _, err := peer.Write(inboundFrame); err != nil {
		t.Fatalf("write inbound frame to TAP peer: %v", err)
	}

	select {
	case <-irqCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the RX IRQ")
	}

	device.withLock(func(mem []byte) {
		id, length := g.usedEntry(int(virtioNetQueueRX), slot)
		if id != 0 {
			t.Errorf("used entry id = %d, want 0", id)
		}
		wantLen := uint32(virtioNetHeaderSize + len(inboundFrame))
		if length != wantLen {
			t.Errorf("used entry len = %d, want %d", length, wantLen)
		}
		gotFrame := mem[rxDataAddr : rxDataAddr+uint64(len(inboundFrame))]
		if string(gotFrame) != string(inboundFrame) {
			t.Errorf("delivered frame = %q, want %q", gotFrame, inboundFrame)
		}
		header := mem[rxHeaderAddr : rxHeaderAddr+virtioNetHeaderSize]
		for i, b := range header {
			if b != 0 {
				t.Errorf("virtio_net_hdr byte %d = %#x, want 0 (no offloads negotiated)", i, b)
			}
		}
	})
}

func TestVirtioNetConfigSpaceReportsMACAndLinkUp(t *testing.T) {
	tapDevice, peer := testTAPPair(t)
	defer peer.Close()
	mac := net.HardwareAddr{0x02, 0x11, 0x22, 0x33, 0x44, 0x55}
	device, stop, err := newVirtioNetMMIODevice(NetworkDeviceOptions{TAP: tapDevice, MAC: mac}, virtioMMIODeviceBaseAddress(0), virtioFirstDeviceIRQ)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	got := make([]byte, 6)
	if err := device.handle(device.base+virtioRegConfigSpace, false, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(mac) {
		t.Fatalf("config space MAC = % x, want % x", got, mac)
	}
	status := make([]byte, 2)
	if err := device.handle(device.base+virtioRegConfigSpace+6, false, status); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint16(status) != virtioNetStatusLinkUp {
		t.Fatalf("config space status = %#x, want VIRTIO_NET_S_LINK_UP", status)
	}
}

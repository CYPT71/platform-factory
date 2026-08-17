//go:build linux && amd64

package kvm

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// These tests are the real-KVM counterpart to TestRunLinuxWithRealKVM,
// specifically for the virtio-mmio devices this file's sibling
// (linux_virtio_blk.go, linux_virtio_net.go) implement: proof against an
// unmodified upstream kernel's own virtio_blk/virtio_net drivers, not just
// this package's own hand-rolled fake-driver unit tests
// (linux_virtio_blk_test.go, linux_virtio_net_test.go). Both skip cleanly
// without PLATFORM_FACTORY_TEST_BZIMAGE/KVM access, the same contract
// TestRunLinuxWithRealKVM already has.

// testRealKVMBoot reads PLATFORM_FACTORY_TEST_BZIMAGE/PLATFORM_FACTORY_TEST_INITRD
// (matching TestRunLinuxWithRealKVM's own contract in
// kvm_run_linux_amd64_test.go) and boots them with options, requiring the
// normal example-service OCI application startup marker to appear - i.e.
// requiring a real, complete, successful guest boot, not just an early
// dmesg fragment - so a virtio device that breaks anything downstream of
// its own probe (an interrupt storm, a hung driver bind, ...) fails this
// the same way it would fail any other real boot.
func testRealKVMBoot(t *testing.T, options LinuxRunOptions) string {
	t.Helper()
	kernelPath := os.Getenv("PLATFORM_FACTORY_TEST_BZIMAGE")
	if kernelPath == "" {
		t.Skip("PLATFORM_FACTORY_TEST_BZIMAGE is not set")
	}
	initrdPath := os.Getenv("PLATFORM_FACTORY_TEST_INITRD")
	if initrdPath == "" {
		t.Skip("PLATFORM_FACTORY_TEST_INITRD is not set")
	}
	requireKVMAccess(t)
	kernel, err := os.ReadFile(kernelPath)
	if err != nil {
		t.Fatal(err)
	}
	initrd, err := os.ReadFile(initrdPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	result, err := RunLinuxWithOptions(ctx, 128<<20, kernel, initrd,
		"console=ttyS0,115200 earlycon=uart,io,0x3f8,115200 ignore_loglevel panic=0 rdinit=/sbin/init",
		1<<20, options)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run Linux: %v; serial=%q", err, result.Serial)
	}
	serial := string(result.Serial)
	if !strings.Contains(serial, `"component":"example-service"`) {
		t.Fatalf("OCI application did not start; serial=%q", serial)
	}
	return serial
}

func TestRunLinuxWithRealKVMVirtioBlk(t *testing.T) {
	backend, err := os.CreateTemp(t.TempDir(), "virtio-blk-kvm-backend")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	const capacity = 8 << 20 // 8 MiB
	if err := backend.Truncate(capacity); err != nil {
		t.Fatal(err)
	}
	seed := []byte("platform-factory-virtio-blk-kvm-proof")
	if _, err := backend.WriteAt(seed, 0); err != nil {
		t.Fatal(err)
	}

	serial := testRealKVMBoot(t, LinuxRunOptions{
		BlockDevices: []BlockDeviceOptions{{Backend: backend, Capacity: capacity}},
	})
	if !strings.Contains(serial, "virtio_blk") || !strings.Contains(serial, "vda") {
		t.Fatalf("guest kernel never recognized the virtio-blk device; serial=%q", serial)
	}
}

// TestRunLinuxWithRealKVMVirtioNet proves the real, functional data path -
// the guest kernel's own virtio_net driver binding, bringing an interface
// up, and transmitting a real Ethernet frame this VMM's device model
// delivers unmodified to the TAP peer. Asserting on dmesg text would only
// prove the transport handshake reached DRIVER_OK, not the data path:
// Linux prints nothing for a quiet, otherwise-unused net_device on a
// udev-less system. `ip=dhcp` makes the kernel's own IP_PNP code bring the
// interface up and broadcast a real DHCPDISCOVER during boot, which this
// test observes arriving on the TAP peer as a real, decoded UDP/68->67
// packet.
func TestRunLinuxWithRealKVMVirtioNet(t *testing.T) {
	kernelPath := os.Getenv("PLATFORM_FACTORY_TEST_BZIMAGE")
	if kernelPath == "" {
		t.Skip("PLATFORM_FACTORY_TEST_BZIMAGE is not set")
	}
	initrdPath := os.Getenv("PLATFORM_FACTORY_TEST_INITRD")
	if initrdPath == "" {
		t.Skip("PLATFORM_FACTORY_TEST_INITRD is not set")
	}
	requireKVMAccess(t)
	kernel, err := os.ReadFile(kernelPath)
	if err != nil {
		t.Fatal(err)
	}
	initrd, err := os.ReadFile(initrdPath)
	if err != nil {
		t.Fatal(err)
	}
	tapDevice, peer := testTAPPair(t)
	defer peer.Close()
	mac := net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x09}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = RunLinuxWithOptions(ctx, 128<<20, kernel, initrd,
			"console=ttyS0,115200 earlycon=uart,io,0x3f8,115200 ignore_loglevel panic=0 rdinit=/sbin/init ip=dhcp",
			1<<20, LinuxRunOptions{NetworkDevices: []NetworkDeviceOptions{{TAP: tapDevice, MAC: mac}}})
	}()

	if err := peer.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 2048)
	foundDHCPDiscover := false
	for !foundDHCPDiscover {
		n, err := peer.Read(buffer)
		if err != nil {
			t.Fatalf("waiting for a DHCPDISCOVER frame from the guest's virtio-net device: %v", err)
		}
		if os.Getenv("PLATFORM_FACTORY_DEBUG_VIRTIO_MMIO") == "1" {
			t.Logf("received frame (%d bytes): % x", n, buffer[:n])
		}
		foundDHCPDiscover = isDHCPDiscoverFrame(buffer[:n])
	}
	cancel()
	<-done
}

// isDHCPDiscoverFrame reports whether frame is a UDP datagram from port 68
// (DHCP client) to port 67 (DHCP server) inside an IPv4-in-Ethernet frame -
// exactly what Linux's IP_PNP DHCP client broadcasts, and specific enough
// that nothing else on an otherwise-silent point-to-point link would
// produce it by coincidence.
func isDHCPDiscoverFrame(frame []byte) bool {
	const (
		etherHeaderLen = 14
		etherTypeIPv4  = 0x0800
		udpProtocol    = 17
		dhcpClientPort = 68
		dhcpServerPort = 67
	)
	if len(frame) < etherHeaderLen+20+8 {
		return false
	}
	if binary.BigEndian.Uint16(frame[12:14]) != etherTypeIPv4 {
		return false
	}
	ip := frame[etherHeaderLen:]
	if ip[9] != udpProtocol {
		return false
	}
	ihl := int(ip[0]&0x0f) * 4
	if ihl < 20 || len(ip) < ihl+8 {
		return false
	}
	udp := ip[ihl:]
	srcPort := binary.BigEndian.Uint16(udp[0:2])
	dstPort := binary.BigEndian.Uint16(udp[2:4])
	return srcPort == dhcpClientPort && dstPort == dhcpServerPort
}

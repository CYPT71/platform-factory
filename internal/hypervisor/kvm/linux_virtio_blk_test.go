//go:build linux && amd64

package kvm

import (
	"encoding/binary"
	"os"
	"testing"
)

// testQueueMem is one virtqueue's descriptor table, avail ring and used
// ring addresses within a testVirtioMem, plus this fake driver's own
// avail/used cursors for it - every queue needs its own independent
// region; sharing one region between two queues (for example virtio-net's
// separate RX and TX queues) would have them silently corrupt each other's
// rings.
type testQueueMem struct {
	descAddr, availAddr, usedAddr uint64
	availIdx, usedIdx             uint16
}

// testVirtioMem lays out one region per queue at fixed, non-overlapping
// offsets inside a flat byte slice standing in for guest physical memory,
// plus a scratch data region above all of them - everything a hand-written
// "fake driver" needs to drive a virtioMMIODevice exactly the way a real
// guest kernel driver would, through the same MMIO register reads/writes,
// without booting a kernel at all.
type testVirtioMem struct {
	mem      []byte
	queues   []testQueueMem
	dataAddr uint64
}

// newTestVirtioMem allocates numQueues independent queue regions - callers
// pass len(device.queues) so every queue the device under test actually
// has gets its own real memory, not just queue 0.
func newTestVirtioMem(numQueues int) *testVirtioMem {
	const perQueueRegion = 0x4000 // desc(4096)+avail(516)+used(2052) rounded well up
	g := &testVirtioMem{mem: make([]byte, 4<<20), dataAddr: 0x100000}
	base := uint64(0x1000)
	for i := 0; i < numQueues; i++ {
		g.queues = append(g.queues, testQueueMem{
			descAddr:  base,
			availAddr: base + 0x1000,
			usedAddr:  base + 0x2000,
		})
		base += perQueueRegion
	}
	return g
}

// alloc reserves n bytes in the scratch data region and returns their
// guest-physical address.
func (g *testVirtioMem) alloc(n int) uint64 {
	addr := g.dataAddr
	g.dataAddr += uint64(n)
	return addr
}

func (g *testVirtioMem) writeDesc(queue int, index uint16, addr uint64, length uint32, flags, next uint16) {
	off := g.queues[queue].descAddr + uint64(index)*virtqDescSize
	binary.LittleEndian.PutUint64(g.mem[off:], addr)
	binary.LittleEndian.PutUint32(g.mem[off+8:], length)
	binary.LittleEndian.PutUint16(g.mem[off+12:], flags)
	binary.LittleEndian.PutUint16(g.mem[off+14:], next)
}

// publish makes the chain starting at headDesc available to the device on
// the given queue and returns the used-ring slot it should watch for the
// completion.
func (g *testVirtioMem) publish(queue int, headDesc uint16) (usedSlot uint16) {
	q := &g.queues[queue]
	slotOffset := q.availAddr + 4 + 2*(uint64(q.availIdx)%uint64(virtioQueueSize))
	binary.LittleEndian.PutUint16(g.mem[slotOffset:], headDesc)
	q.availIdx++
	binary.LittleEndian.PutUint16(g.mem[q.availAddr+2:], q.availIdx)
	slot := q.usedIdx
	q.usedIdx++
	return slot
}

func (g *testVirtioMem) usedEntry(queue int, slot uint16) (id, length uint32) {
	off := g.queues[queue].usedAddr + 4 + 8*uint64(slot)
	return binary.LittleEndian.Uint32(g.mem[off:]), binary.LittleEndian.Uint32(g.mem[off+4:])
}

// reg32 does one MMIO register access through device.handle, exactly the
// shape a KVM_EXIT_MMIO for a 4-byte access would take.
func reg32(t *testing.T, device *virtioMMIODevice, offset uint64, write bool, value uint32) uint32 {
	t.Helper()
	buf := make([]byte, 4)
	if write {
		binary.LittleEndian.PutUint32(buf, value)
	}
	if err := device.handle(device.base+offset, write, buf); err != nil {
		t.Fatalf("MMIO %s at offset %#x: %v", map[bool]string{true: "write", false: "read"}[write], offset, err)
	}
	return binary.LittleEndian.Uint32(buf)
}

// driveToDriverOK runs the standard virtio device initialization sequence
// (VIRTIO 1.2 section 3.1.1) against device: reset, ACKNOWLEDGE, DRIVER,
// negotiate every feature the device offers, FEATURES_OK, configure every
// one of the device's queues to point at its own region in g (g must have
// been constructed with newTestVirtioMem(len(device.queues))), DRIVER_OK.
// Real drivers negotiate a feature subset; accepting everything offered is
// simplest here and every device in this package offers only bits it can
// actually honor unconditionally.
func driveToDriverOK(t *testing.T, device *virtioMMIODevice, g *testVirtioMem) {
	t.Helper()
	if magic := reg32(t, device, virtioRegMagicValue, false, 0); magic != virtioMMIOMagicValue {
		t.Fatalf("MagicValue = %#x, want %#x", magic, virtioMMIOMagicValue)
	}
	if version := reg32(t, device, virtioRegVersion, false, 0); version != virtioMMIOVersion {
		t.Fatalf("Version = %d, want %d", version, virtioMMIOVersion)
	}
	reg32(t, device, virtioRegStatus, true, 0)
	reg32(t, device, virtioRegStatus, true, virtioStatusAcknowledge)
	reg32(t, device, virtioRegStatus, true, virtioStatusAcknowledge|virtioStatusDriver)

	reg32(t, device, virtioRegDeviceFeaturesSel, true, 0)
	low := reg32(t, device, virtioRegDeviceFeatures, false, 0)
	reg32(t, device, virtioRegDeviceFeaturesSel, true, 1)
	high := reg32(t, device, virtioRegDeviceFeatures, false, 0)
	if high&(1<<0) == 0 { // bit 32 overall = bit 0 of the high word: VIRTIO_F_VERSION_1
		t.Fatal("device did not offer VIRTIO_F_VERSION_1")
	}
	reg32(t, device, virtioRegDriverFeaturesSel, true, 0)
	reg32(t, device, virtioRegDriverFeatures, true, low)
	reg32(t, device, virtioRegDriverFeaturesSel, true, 1)
	reg32(t, device, virtioRegDriverFeatures, true, high)

	reg32(t, device, virtioRegStatus, true, virtioStatusAcknowledge|virtioStatusDriver|virtioStatusFeaturesOK)
	if status := reg32(t, device, virtioRegStatus, false, 0); status&virtioStatusFeaturesOK == 0 {
		t.Fatal("device rejected offered features (FEATURES_OK did not stick)")
	}

	if len(g.queues) != len(device.queues) {
		t.Fatalf("test memory has %d queue regions, device has %d queues", len(g.queues), len(device.queues))
	}
	for i, q := range g.queues {
		reg32(t, device, virtioRegQueueSel, true, uint32(i))
		if max := reg32(t, device, virtioRegQueueNumMax, false, 0); max == 0 {
			t.Fatalf("QueueNumMax is 0 for queue %d", i)
		}
		reg32(t, device, virtioRegQueueNum, true, uint32(virtioQueueSize))
		reg32(t, device, virtioRegQueueDescLow, true, uint32(q.descAddr))
		reg32(t, device, virtioRegQueueDescHigh, true, uint32(q.descAddr>>32))
		reg32(t, device, virtioRegQueueDriverLow, true, uint32(q.availAddr))
		reg32(t, device, virtioRegQueueDriverHigh, true, uint32(q.availAddr>>32))
		reg32(t, device, virtioRegQueueDeviceLow, true, uint32(q.usedAddr))
		reg32(t, device, virtioRegQueueDeviceHigh, true, uint32(q.usedAddr>>32))
		reg32(t, device, virtioRegQueueReady, true, 1)
	}

	reg32(t, device, virtioRegStatus, true, virtioStatusAcknowledge|virtioStatusDriver|virtioStatusFeaturesOK|virtioStatusDriverOK)
}

func TestVirtioBlkReadWriteRoundTrip(t *testing.T) {
	backingFile, err := os.CreateTemp(t.TempDir(), "virtio-blk-backend")
	if err != nil {
		t.Fatal(err)
	}
	defer backingFile.Close()
	const capacity = 64 * 512 // 64 sectors
	if err := backingFile.Truncate(capacity); err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, 512)
	for i := range seed {
		seed[i] = byte(i)
	}
	if _, err := backingFile.WriteAt(seed, 3*512); err != nil {
		t.Fatal(err)
	}

	device, err := newVirtioBlkMMIODevice(BlockDeviceOptions{Backend: backingFile, Capacity: capacity}, virtioMMIODeviceBaseAddress(0), virtioFirstDeviceIRQ)
	if err != nil {
		t.Fatalf("newVirtioBlkMMIODevice: %v", err)
	}
	g := newTestVirtioMem(len(device.queues))
	device.guestMemory = g.mem
	var irqCount int
	device.raiseIRQ = func() { irqCount++ }

	driveToDriverOK(t, device, g)
	if deviceID := reg32(t, device, virtioRegDeviceID, false, 0); deviceID != virtioDeviceIDBlock {
		t.Fatalf("DeviceID = %d, want %d", deviceID, virtioDeviceIDBlock)
	}

	// VIRTIO_BLK_T_IN: read sector 3 (where seed was written) into a
	// fresh, device-writable buffer.
	headerAddr := g.alloc(16)
	binary.LittleEndian.PutUint32(g.mem[headerAddr:], virtioBlkTypeIn)
	binary.LittleEndian.PutUint64(g.mem[headerAddr+8:], 3) // sector
	dataAddr := g.alloc(512)
	statusAddr := g.alloc(1)
	g.writeDesc(0, 0, headerAddr, 16, virtqDescFlagNext, 1)
	g.writeDesc(0, 1, dataAddr, 512, virtqDescFlagNext|virtqDescFlagWrite, 2)
	g.writeDesc(0, 2, statusAddr, 1, virtqDescFlagWrite, 0)
	slot := g.publish(0, 0)

	reg32(t, device, virtioRegQueueNotify, true, 0)

	if got := g.mem[statusAddr]; got != virtioBlkStatusOK {
		t.Fatalf("status byte = %d, want VIRTIO_BLK_S_OK", got)
	}
	if got := g.mem[dataAddr : dataAddr+512]; string(got) != string(seed) {
		t.Fatalf("read data does not match what was written to the backend")
	}
	if id, length := g.usedEntry(0, slot); id != 0 || length != 513 {
		t.Fatalf("used entry = {id:%d len:%d}, want {id:0 len:513}", id, length)
	}
	if irqCount != 1 {
		t.Fatalf("raiseIRQ called %d times, want 1", irqCount)
	}
	if device.interruptStatus&virtioInterruptUsedRing == 0 {
		t.Fatal("InterruptStatus used-ring bit not set after a completed request")
	}
	ackBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(ackBuf, virtioInterruptUsedRing)
	if err := device.handle(device.base+virtioRegInterruptACK, true, ackBuf); err != nil {
		t.Fatal(err)
	}
	if device.interruptStatus != 0 {
		t.Fatalf("InterruptStatus = %#x after ACK, want 0", device.interruptStatus)
	}

	// VIRTIO_BLK_T_OUT: write a new pattern to sector 10, then read it
	// back directly from the backing file to prove it actually landed.
	outHeaderAddr := g.alloc(16)
	binary.LittleEndian.PutUint32(g.mem[outHeaderAddr:], virtioBlkTypeOut)
	binary.LittleEndian.PutUint64(g.mem[outHeaderAddr+8:], 10)
	outDataAddr := g.alloc(512)
	pattern := make([]byte, 512)
	for i := range pattern {
		pattern[i] = 0xaa
	}
	copy(g.mem[outDataAddr:], pattern)
	outStatusAddr := g.alloc(1)
	g.writeDesc(0, 3, outHeaderAddr, 16, virtqDescFlagNext, 4)
	g.writeDesc(0, 4, outDataAddr, 512, virtqDescFlagNext, 5) // device-readable: no F_WRITE
	g.writeDesc(0, 5, outStatusAddr, 1, virtqDescFlagWrite, 0)
	outSlot := g.publish(0, 3)

	reg32(t, device, virtioRegQueueNotify, true, 0)

	if got := g.mem[outStatusAddr]; got != virtioBlkStatusOK {
		t.Fatalf("OUT status byte = %d, want VIRTIO_BLK_S_OK", got)
	}
	if id, length := g.usedEntry(0, outSlot); id != 3 || length != 1 {
		t.Fatalf("OUT used entry = {id:%d len:%d}, want {id:3 len:1}", id, length)
	}
	readBack := make([]byte, 512)
	if _, err := backingFile.ReadAt(readBack, 10*512); err != nil {
		t.Fatal(err)
	}
	if string(readBack) != string(pattern) {
		t.Fatal("write request did not actually reach the backing file")
	}
}

func TestVirtioBlkReadOnlyRejectsWrites(t *testing.T) {
	backingFile, err := os.CreateTemp(t.TempDir(), "virtio-blk-ro-backend")
	if err != nil {
		t.Fatal(err)
	}
	defer backingFile.Close()
	if err := backingFile.Truncate(4096); err != nil {
		t.Fatal(err)
	}
	device, err := newVirtioBlkMMIODevice(BlockDeviceOptions{Backend: backingFile, Capacity: 4096, ReadOnly: true}, virtioMMIODeviceBaseAddress(0), virtioFirstDeviceIRQ)
	if err != nil {
		t.Fatal(err)
	}
	g := newTestVirtioMem(len(device.queues))
	device.guestMemory = g.mem
	device.raiseIRQ = func() {}
	driveToDriverOK(t, device, g)

	headerAddr := g.alloc(16)
	binary.LittleEndian.PutUint32(g.mem[headerAddr:], virtioBlkTypeOut)
	dataAddr := g.alloc(512)
	statusAddr := g.alloc(1)
	g.writeDesc(0, 0, headerAddr, 16, virtqDescFlagNext, 1)
	g.writeDesc(0, 1, dataAddr, 512, virtqDescFlagNext, 2)
	g.writeDesc(0, 2, statusAddr, 1, virtqDescFlagWrite, 0)
	g.publish(0, 0)

	reg32(t, device, virtioRegQueueNotify, true, 0)

	if got := g.mem[statusAddr]; got != virtioBlkStatusIOErr {
		t.Fatalf("status byte = %d, want VIRTIO_BLK_S_IOERR for a write to a read-only device", got)
	}
}

func TestVirtioBlkRejectsBadCapacity(t *testing.T) {
	backingFile, err := os.CreateTemp(t.TempDir(), "virtio-blk-bad-capacity")
	if err != nil {
		t.Fatal(err)
	}
	defer backingFile.Close()
	if _, err := newVirtioBlkMMIODevice(BlockDeviceOptions{Backend: backingFile, Capacity: 100}, virtioMMIODeviceBaseAddress(0), virtioFirstDeviceIRQ); err == nil {
		t.Fatal("expected an error for a capacity that is not a multiple of the 512-byte sector size")
	}
}

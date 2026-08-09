//go:build linux && amd64

package kvm

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// This file implements the virtio split virtqueue (VIRTIO 1.2 spec section
// 2.7) directly against guest physical memory - the same flat []byte this
// VMM already uses for everything else (Linux boot params, the E820 map,
// kernel/initrd placement). No indirect descriptors and no VIRTIO_F_EVENT_IDX:
// neither feature bit is ever offered (see virtioFeatures in
// linux_virtio_mmio.go), so a compliant driver never uses either, and this
// engine does not need to implement them.
const (
	// virtq_desc: le64 addr; le32 len; le16 flags; le16 next. (16 bytes)
	virtqDescSize = 16
	// virtq_avail: le16 flags; le16 idx; le16 ring[size]; le16 used_event.
	virtqAvailRingOffset = 4
	// virtq_used: le16 flags; le16 idx; struct{le32 id; le32 len} ring[size]; le16 avail_event.
	virtqUsedRingOffset   = 4
	virtqUsedElemSize     = 8
	virtqDescFlagNext     = uint16(1)
	virtqDescFlagWrite    = uint16(2)
	virtqDescFlagIndirect = uint16(4)
	// A malicious or buggy driver could otherwise chain descriptors into a
	// cycle and hang this goroutine forever walking it.
	virtqMaxChainLength = 1024
)

// virtQueue tracks one virtio-mmio queue's negotiated size and the three
// guest-physical addresses the driver programmed for it (QueueDescLow/High,
// QueueDriverLow/High, QueueDeviceLow/High in the transport - see
// linux_virtio_mmio.go). lastAvail is this device's own cursor into the
// avail ring, never written by the guest.
type virtQueue struct {
	size      uint16
	ready     bool
	descAddr  uint64
	availAddr uint64
	usedAddr  uint64
	lastAvail uint16
}

// virtqDescriptor is one decoded, bounds-checked descriptor: buf is a direct
// slice into guest memory (addr..addr+len), aliasing it rather than copying.
type virtqDescriptor struct {
	buf         []byte
	deviceWrite bool // VIRTQ_DESC_F_WRITE: device may write into buf
}

func leUint16(mem []byte, addr uint64) uint16       { return binary.LittleEndian.Uint16(mem[addr:]) }
func leUint32(mem []byte, addr uint64) uint32       { return binary.LittleEndian.Uint32(mem[addr:]) }
func putLeUint16(mem []byte, addr uint64, v uint16) { binary.LittleEndian.PutUint16(mem[addr:], v) }
func putLeUint32(mem []byte, addr uint64, v uint32) { binary.LittleEndian.PutUint32(mem[addr:], v) }

// guestRange bounds-checks [addr, addr+length) against mem and returns the
// aliased slice. Every guest-supplied address/length in this file goes
// through this - the driver is semi-trusted at best (a compromised or buggy
// guest kernel/driver must not be able to read or write outside its own
// assigned memory via a crafted descriptor).
func guestRange(mem []byte, addr uint64, length uint64) ([]byte, error) {
	if length == 0 {
		return mem[:0], nil
	}
	if addr > uint64(len(mem)) || length > uint64(len(mem))-addr {
		return nil, fmt.Errorf("vmm: virtio: guest address range [%#x,+%#x) exceeds guest memory", addr, length)
	}
	return mem[addr : addr+length], nil
}

// availIdx reads the avail ring's current idx - how many descriptor chains
// the driver has ever made available, mod 2^16, monotonically increasing
// for the life of the queue.
func (q *virtQueue) availIdx(mem []byte) (uint16, error) {
	field, err := guestRange(mem, q.availAddr+2, 2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(field), nil
}

// pending reports whether the driver has made any descriptor chains
// available since this device last drained the ring.
func (q *virtQueue) pending(mem []byte) (bool, error) {
	idx, err := q.availIdx(mem)
	if err != nil {
		return false, err
	}
	return idx != q.lastAvail, nil
}

// popAvail returns the head descriptor index of the next available chain
// and advances this device's own cursor, or ok=false if the ring is empty.
func (q *virtQueue) popAvail(mem []byte) (head uint16, ok bool, err error) {
	idx, err := q.availIdx(mem)
	if err != nil {
		return 0, false, err
	}
	if idx == q.lastAvail {
		return 0, false, nil
	}
	if q.size == 0 {
		return 0, false, errors.New("vmm: virtio: queue size is zero")
	}
	slot := q.availAddr + virtqAvailRingOffset + 2*uint64(q.lastAvail%q.size)
	field, err := guestRange(mem, slot, 2)
	if err != nil {
		return 0, false, err
	}
	head = binary.LittleEndian.Uint16(field)
	q.lastAvail++
	return head, true, nil
}

// readChain walks the descriptor chain starting at head and returns each
// descriptor's bounds-checked, guest-memory-aliased buffer in order.
func (q *virtQueue) readChain(mem []byte, head uint16) ([]virtqDescriptor, error) {
	var chain []virtqDescriptor
	index := head
	for i := 0; ; i++ {
		if i >= virtqMaxChainLength {
			return nil, fmt.Errorf("vmm: virtio: descriptor chain exceeds %d entries (cycle?)", virtqMaxChainLength)
		}
		if index >= q.size {
			return nil, fmt.Errorf("vmm: virtio: descriptor index %d out of range for queue size %d", index, q.size)
		}
		descOff := q.descAddr + uint64(index)*virtqDescSize
		raw, err := guestRange(mem, descOff, virtqDescSize)
		if err != nil {
			return nil, err
		}
		addr := binary.LittleEndian.Uint64(raw[0:8])
		length := binary.LittleEndian.Uint32(raw[8:12])
		flags := binary.LittleEndian.Uint16(raw[12:14])
		next := binary.LittleEndian.Uint16(raw[14:16])
		if flags&virtqDescFlagIndirect != 0 {
			return nil, errors.New("vmm: virtio: indirect descriptors are not supported (VIRTIO_F_INDIRECT_DESC was never offered)")
		}
		buf, err := guestRange(mem, addr, uint64(length))
		if err != nil {
			return nil, err
		}
		chain = append(chain, virtqDescriptor{buf: buf, deviceWrite: flags&virtqDescFlagWrite != 0})
		if flags&virtqDescFlagNext == 0 {
			break
		}
		index = next
	}
	return chain, nil
}

// pushUsed records that the chain starting at descriptor id has been
// completed with writtenLen bytes written into its device-writable
// descriptors (0 for chains with none, e.g. a pure write/OUT request), then
// advances the used ring's idx. The idx update is the last write, matching
// the driver-visible ordering the spec requires (id/len must be visible
// before idx advances past them).
func (q *virtQueue) pushUsed(mem []byte, id uint32, writtenLen uint32) error {
	if q.size == 0 {
		return errors.New("vmm: virtio: queue size is zero")
	}
	usedIdxField, err := guestRange(mem, q.usedAddr+2, 2)
	if err != nil {
		return err
	}
	usedIdx := binary.LittleEndian.Uint16(usedIdxField)
	elemOff := q.usedAddr + virtqUsedRingOffset + virtqUsedElemSize*uint64(usedIdx%q.size)
	elem, err := guestRange(mem, elemOff, virtqUsedElemSize)
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(elem[0:4], id)
	binary.LittleEndian.PutUint32(elem[4:8], writtenLen)
	binary.LittleEndian.PutUint16(usedIdxField, usedIdx+1)
	return nil
}

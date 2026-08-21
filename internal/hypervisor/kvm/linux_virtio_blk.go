//go:build linux && amd64

package kvm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// This file implements virtio-blk (VIRTIO 1.2 spec section 5.2) against
// the split-virtqueue engine in linux_virtqueue.go and the virtio-mmio
// transport in linux_virtio_mmio.go. One request queue, matching
// BlockDeviceOptions' single Backend per device - there is no benefit to
// multiqueue for a single-vCPU guest with one Go goroutine servicing every
// request synchronously on the vCPU thread.
const (
	virtioDeviceIDBlock = uint32(2)

	// VIRTIO_BLK_F_RO / VIRTIO_BLK_F_BLK_SIZE / VIRTIO_BLK_F_FLUSH bits
	// (spec 5.2.3). Every other optional feature (multiqueue, discard,
	// write-zeroes, geometry/topology hints, ...) is deliberately never
	// offered: a compliant driver only acts on a feature it saw offered
	// and then negotiated, so simply not offering one is sufficient to
	// keep this device out of code paths it doesn't implement.
	virtioBlkFeatureRO      = uint64(1) << 5
	virtioBlkFeatureBlkSize = uint64(1) << 6
	virtioBlkFeatureFlush   = uint64(1) << 9

	virtioBlkTypeIn       = uint32(0)
	virtioBlkTypeOut      = uint32(1)
	virtioBlkTypeFlush    = uint32(4)
	virtioBlkTypeGetID    = uint32(8)
	virtioBlkStatusOK     = byte(0)
	virtioBlkStatusIOErr  = byte(1)
	virtioBlkStatusUnsupp = byte(2)

	virtioBlkSectorSize = uint64(512)
	virtioBlkIDBytes    = 20
)

// virtioBlkDevice is the device-specific state a virtioMMIODevice's notify
// callback closes over: the backing store and whether writes are allowed.
// It holds no reference to guest memory itself - every call into it
// receives the already-bounds-checked descriptor chain for one request.
type virtioBlkDevice struct {
	backend  BlockBackend
	readOnly bool
}

// newVirtioBlkMMIODevice builds the transport-level virtioMMIODevice for
// one BlockDeviceOptions, at the given MMIO window/IRQ, wired to process
// requests via processVirtioBlkQueue whenever the driver notifies queue 0.
func newVirtioBlkMMIODevice(opts BlockDeviceOptions, base uint64, irq uint32) (*virtioMMIODevice, error) {
	if opts.Backend == nil {
		return nil, errors.New("vmm: virtio-blk: Backend is required")
	}
	if opts.Capacity == 0 || opts.Capacity%virtioBlkSectorSize != 0 {
		return nil, fmt.Errorf("vmm: virtio-blk: capacity %d must be a positive multiple of %d", opts.Capacity, virtioBlkSectorSize)
	}
	config := make([]byte, 24)
	binary.LittleEndian.PutUint64(config[0:8], opts.Capacity/virtioBlkSectorSize)
	binary.LittleEndian.PutUint32(config[20:24], uint32(virtioBlkSectorSize)) // blk_size, offset per struct virtio_blk_config

	features := virtioBlkFeatureBlkSize | virtioBlkFeatureFlush
	if opts.ReadOnly {
		features |= virtioBlkFeatureRO
	}

	blk := &virtioBlkDevice{backend: opts.Backend, readOnly: opts.ReadOnly}
	device := newVirtioMMIODevice("virtio-blk", virtioDeviceIDBlock, base, irq, 1, features, config)
	device.notify = func(queueIndex uint32) error {
		q, ok := device.queueReadyForIO(queueIndex)
		if !ok {
			return nil
		}
		return blk.processQueue(device, q)
	}
	return device, nil
}

// processQueue drains every descriptor chain the driver has made available
// on q since the last notify (a driver may batch several requests behind
// one doorbell write), completing each in order.
func (blk *virtioBlkDevice) processQueue(device *virtioMMIODevice, q *virtQueue) error {
	mem := device.guestMemory
	processed := false
	for {
		pending, err := q.pending(mem)
		if err != nil {
			return err
		}
		if !pending {
			break
		}
		head, ok, err := q.popAvail(mem)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		chain, err := q.readChain(mem, head)
		if err != nil {
			return err
		}
		written, err := blk.handleRequest(chain)
		if err != nil {
			return err
		}
		if err := q.pushUsed(mem, uint32(head), written); err != nil {
			return err
		}
		processed = true
	}
	if processed {
		device.signalUsedRing()
	}
	return nil
}

// handleRequest services exactly one virtio-blk request chain: a
// device-readable virtio_blk_outhdr, zero or more data descriptors, and a
// trailing device-writable status byte (spec 5.2.6.2). It always writes a
// status byte - VIRTIO_BLK_S_UNSUPP for a request type this device does
// not implement, VIRTIO_BLK_S_IOERR for a backend I/O failure on any
// request type including VIRTIO_BLK_T_FLUSH - rather than returning an
// error for anything the guest itself controls or any failure the guest's
// own backing storage can produce: a malformed/unsupported *request* is
// the guest driver's problem, and a failed flush is the same kind of
// storage-level failure a real disk reports, both handled back to the
// guest exactly as the spec provides for, not a VMM-level fault. An error
// return here is reserved for guest memory the descriptor chain itself
// points outside of, which readChain already catches before handleRequest
// ever sees it.
func (blk *virtioBlkDevice) handleRequest(chain []virtqDescriptor) (uint32, error) {
	if len(chain) < 2 {
		return 0, errors.New("vmm: virtio-blk: request chain has fewer than the required header+status descriptors")
	}
	header := chain[0].buf
	if len(header) < 16 {
		return 0, errors.New("vmm: virtio-blk: request header shorter than struct virtio_blk_outhdr")
	}
	status := chain[len(chain)-1]
	if len(status.buf) < 1 || !status.deviceWrite {
		return 0, errors.New("vmm: virtio-blk: request has no writable trailing status byte")
	}
	data := chain[1 : len(chain)-1]

	reqType := binary.LittleEndian.Uint32(header[0:4])
	sector := binary.LittleEndian.Uint64(header[8:16])
	offset := int64(sector) * int64(virtioBlkSectorSize)

	var written uint32
	result := virtioBlkStatusOK
	switch reqType {
	case virtioBlkTypeIn:
		for _, d := range data {
			if !d.deviceWrite {
				result = virtioBlkStatusIOErr
				break
			}
			n, err := blk.backend.ReadAt(d.buf, offset)
			written += uint32(n)
			offset += int64(n)
			// ReaderAt must fill the requested descriptor. A short read is an
			// I/O failure even when the backend reports the conventional EOF;
			// otherwise stale guest memory would be presented as valid disk data.
			if n != len(d.buf) || (err != nil && !errors.Is(err, io.EOF)) {
				result = virtioBlkStatusIOErr
				break
			}
		}
	case virtioBlkTypeOut:
		if blk.readOnly {
			result = virtioBlkStatusIOErr
			break
		}
		for _, d := range data {
			n, err := blk.backend.WriteAt(d.buf, offset)
			offset += int64(n)
			if n != len(d.buf) || err != nil {
				result = virtioBlkStatusIOErr
				break
			}
		}
	case virtioBlkTypeFlush:
		if err := blk.backend.Sync(); err != nil {
			result = virtioBlkStatusIOErr
		}
	case virtioBlkTypeGetID:
		if len(data) != 1 || !data[0].deviceWrite {
			result = virtioBlkStatusUnsupp
			break
		}
		id := "platform-factory-vd"
		n := copy(data[0].buf, id)
		clear(data[0].buf[n:])
		if len(data[0].buf) > virtioBlkIDBytes {
			written = virtioBlkIDBytes
		} else {
			written = uint32(len(data[0].buf))
		}
	default:
		result = virtioBlkStatusUnsupp
	}
	status.buf[0] = result
	return written + 1, nil
}

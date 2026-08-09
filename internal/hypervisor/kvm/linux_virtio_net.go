//go:build linux && amd64

package kvm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
)

// This file implements virtio-net (VIRTIO 1.2 spec section 5.1) against
// the split-virtqueue engine in linux_virtqueue.go and the virtio-mmio
// transport in linux_virtio_mmio.go, backed by an already-open TAP
// descriptor (see linux_tap_linux.go's OpenTAP). No offloads are
// negotiated (no checksum, no GSO, no VIRTIO_NET_F_MRG_RXBUF), so every
// packet - guest-to-host (TX) or host-to-guest (RX) - carries exactly the
// fixed virtioNetHeaderSize virtio_net_hdr this file's constant describes,
// immediately followed by one raw Ethernet frame.
const (
	virtioDeviceIDNet = uint32(1)

	// Per spec 5.1.2: "Queue 0 is always used for receiveq1... Queue 1 is
	// always used for transmitq1", regardless of how many queue pairs a
	// multiqueue device might have (this device has exactly one pair).
	virtioNetQueueRX = uint32(0)
	virtioNetQueueTX = uint32(1)

	// 12 bytes (struct virtio_net_hdr_mrg_rxbuf), not the 10-byte plain
	// struct virtio_net_hdr the name might suggest: every device this
	// package creates unconditionally offers VIRTIO_F_VERSION_1
	// (newVirtioMMIODevice adds virtioFeatureVersion1 to every device's
	// features), and Linux's own virtio_net driver
	// (drivers/net/virtio_net.c, virtnet_probe) switches to the 12-byte
	// mrg_rxbuf header layout whenever VIRTIO_F_VERSION_1 is negotiated,
	// regardless of whether VIRTIO_NET_F_MRG_RXBUF itself was ever
	// offered - the extra num_buffers field is simply always present in
	// the modern transport's wire layout, unused unless MRG_RXBUF is
	// also negotiated.
	virtioNetHeaderSize = 12
	// 1514 (max standard untagged Ethernet frame) rounded up generously;
	// large enough for anything the guest's 1500-byte-MTU virtio-net
	// interface will ever hand this device, small enough to bound one
	// read from the TAP fd to a single frame.
	virtioNetMaxFrame = 65536

	// VIRTIO_NET_S_LINK_UP (config space `status` field, spec 5.1.4):
	// reported as always up rather than tracking TAP interface state -
	// this VMM does not currently reflect host-side link changes into
	// the guest at all, and "always up" is what its predecessor (no
	// virtio-net device at all) effectively meant for a guest that only
	// ever sees connectivity work or not.
	virtioNetStatusLinkUp = uint16(1)
)

// virtioNetDevice is the device-specific state a virtioMMIODevice's notify
// callback and RX goroutine both close over.
type virtioNetDevice struct {
	tap    *os.File
	stopRX chan struct{}
	rxDone chan struct{}
}

// newVirtioNetMMIODevice builds the transport-level virtioMMIODevice for
// one NetworkDeviceOptions, at the given MMIO window/IRQ. It starts a
// background goroutine (readTAPLoop) that runs for the lifetime of the
// returned device and must be stopped by calling the returned stop
// function once the guest is done running - RunLinuxWithOptions does this
// via a defer, the same pattern it already uses for guestChannel.
func newVirtioNetMMIODevice(opts NetworkDeviceOptions, base uint64, irq uint32) (device *virtioMMIODevice, stop func(), err error) {
	if opts.TAP == nil {
		return nil, nil, errors.New("vmm: virtio-net: TAP descriptor is required")
	}
	if len(opts.MAC) != 6 {
		return nil, nil, fmt.Errorf("vmm: virtio-net: MAC must be 6 bytes, got %d", len(opts.MAC))
	}

	config := make([]byte, 8)
	copy(config[0:6], opts.MAC)
	binary.LittleEndian.PutUint16(config[6:8], virtioNetStatusLinkUp)

	// VIRTIO_NET_F_MAC (bit 5): the device's MAC comes from config space
	// rather than being driver-assigned. VIRTIO_NET_F_STATUS (bit 16):
	// the guest driver reads config space `status` instead of assuming
	// link is always up - offered mainly so ethtool/ip link inside the
	// guest report something meaningful rather than an absent field.
	const (
		virtioNetFeatureMAC    = uint64(1) << 5
		virtioNetFeatureStatus = uint64(1) << 16
	)

	net := &virtioNetDevice{tap: opts.TAP, stopRX: make(chan struct{}), rxDone: make(chan struct{})}
	device = newVirtioMMIODevice("virtio-net", virtioDeviceIDNet, base, irq, 2, virtioNetFeatureMAC|virtioNetFeatureStatus, config)
	device.notify = func(queueIndex uint32) error {
		if queueIndex != virtioNetQueueTX {
			return nil
		}
		q, ok := device.queueReadyForIO(queueIndex)
		if !ok {
			return nil
		}
		return net.transmit(device, q)
	}

	go net.readTAPLoop(device)
	stop = func() {
		close(net.stopRX)
		_ = net.tap.Close() // unblocks the pending Read in readTAPLoop
		<-net.rxDone
	}
	return device, stop, nil
}

// transmit drains every TX descriptor chain the driver has made available
// since the last notify, writing each one's Ethernet frame (the bytes
// after the fixed virtio_net_hdr) to the TAP device as one host-side
// packet write.
func (n *virtioNetDevice) transmit(device *virtioMMIODevice, q *virtQueue) error {
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
		if err := n.transmitChain(chain); err != nil {
			return err
		}
		if err := q.pushUsed(mem, uint32(head), 0); err != nil {
			return err
		}
		processed = true
	}
	if processed {
		device.signalUsedRing()
	}
	return nil
}

// transmitChain reassembles one packet from a TX descriptor chain (header
// descriptor(s) then frame data, however the driver happened to split
// them across descriptors - nothing requires a single descriptor per
// packet) and writes it to the TAP device in one call, which is what a
// TAP/TUN character device requires: each write(2) is exactly one frame,
// never a partial one.
func (n *virtioNetDevice) transmitChain(chain []virtqDescriptor) error {
	var total int
	for _, d := range chain {
		total += len(d.buf)
	}
	if total < virtioNetHeaderSize {
		return errors.New("vmm: virtio-net: TX chain shorter than virtio_net_hdr")
	}
	frame := make([]byte, 0, total-virtioNetHeaderSize)
	skip := virtioNetHeaderSize
	for _, d := range chain {
		buf := d.buf
		if skip > 0 {
			if skip >= len(buf) {
				skip -= len(buf)
				continue
			}
			buf = buf[skip:]
			skip = 0
		}
		frame = append(frame, buf...)
	}
	if len(frame) == 0 {
		return nil
	}
	_, err := n.tap.Write(frame)
	if err != nil {
		return fmt.Errorf("vmm: virtio-net: write TAP frame: %w", err)
	}
	return nil
}

// readTAPLoop is this device's only source of inbound (RX) traffic: it
// blocks reading whole frames from the TAP device and, for each one,
// delivers it into the next available RX descriptor chain under
// device.mu - see that field's doc comment (linux_virtio_mmio.go) for why
// this needs its own lock instead of relying on the vCPU thread being the
// only writer. A frame that arrives with no RX buffer currently posted by
// the driver is dropped, the same "no space, drop it" behavior any real
// NIC has under equivalent backpressure.
func (n *virtioNetDevice) readTAPLoop(device *virtioMMIODevice) {
	defer close(n.rxDone)
	buffer := make([]byte, virtioNetMaxFrame)
	for {
		count, err := n.tap.Read(buffer)
		if err != nil {
			select {
			case <-n.stopRX:
				return
			default:
			}
			if count <= 0 {
				return
			}
		}
		if count <= 0 {
			continue
		}
		frame := buffer[:count]
		device.withLock(func(mem []byte) {
			select {
			case <-n.stopRX:
				return
			default:
			}
			q, ok := device.queueReadyForIO(virtioNetQueueRX)
			if !ok {
				return
			}
			n.deliverLocked(device, mem, q, frame)
		})
	}
}

// deliverLocked writes one inbound frame into the next available RX
// descriptor chain. Called with device.mu already held.
func (n *virtioNetDevice) deliverLocked(device *virtioMMIODevice, mem []byte, q *virtQueue, frame []byte) {
	pending, err := q.pending(mem)
	if err != nil || !pending {
		return // no posted RX buffer - drop, matching a real NIC under backpressure
	}
	head, ok, err := q.popAvail(mem)
	if err != nil || !ok {
		return
	}
	chain, err := q.readChain(mem, head)
	if err != nil {
		return
	}
	var total int
	for _, d := range chain {
		total += len(d.buf)
	}
	if total < virtioNetHeaderSize+len(frame) {
		// The driver's posted buffer is smaller than this frame; nothing
		// safe to do but drop it and still complete the chain with 0
		// bytes written so it goes back to the driver's free list.
		_ = q.pushUsed(mem, uint32(head), 0)
		device.signalUsedRing()
		return
	}
	written := 0
	remaining := append([]byte(nil), make([]byte, virtioNetHeaderSize)...) // all-zero virtio_net_hdr: no offloads negotiated
	remaining = append(remaining, frame...)
	for _, d := range chain {
		if len(remaining) == 0 {
			break
		}
		if !d.deviceWrite {
			continue
		}
		copied := copy(d.buf, remaining)
		remaining = remaining[copied:]
		written += copied
	}
	_ = q.pushUsed(mem, uint32(head), uint32(written))
	device.signalUsedRing()
}

// virtioNetMACFromInterfaceIndex is a small helper callers can use to
// derive a stable, locally-administered MAC for a guest's virtio-net
// device from an arbitrary small integer (for example a per-container
// sequence number) without needing real hardware or a random source at
// call time. Not used by this file directly; kept next to the device it
// exists for.
func virtioNetMACFromInterfaceIndex(index uint32) net.HardwareAddr {
	mac := make(net.HardwareAddr, 6)
	mac[0] = 0x02 // locally administered, unicast (U/L and I/G bits)
	mac[1] = 0x00
	binary.BigEndian.PutUint32(mac[2:6], index)
	return mac
}

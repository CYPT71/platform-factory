//go:build linux && amd64

package kvm

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"
)

// This file implements the virtio-mmio transport (VIRTIO 1.2 spec section
// 4.2), version 2 (modern) only - never the legacy version-1 register
// layout. It is the transport, not virtio-pci, because this VMM has no PCI
// host bridge at all: linux_pci.go deliberately keeps the guest's PCI
// config-space probe permanently negative. x86 also has no ACPI/devicetree
// path to auto-discover MMIO devices, so every device this VMM registers
// must additionally be named on the kernel command line via
// `virtio_mmio.device=<size>@<base>:<irq>` (CONFIG_VIRTIO_MMIO_CMDLINE_DEVICES) -
// see virtioMMIOCommandLineParam.
const (
	// The fixed magic value every virtio-mmio device's register 0 reads as
	// (VIRTIO 1.2 section 4.2.2): the bytes 'v','i','r','t' read as a
	// little-endian 32-bit word.
	virtioMMIOMagicValue = uint32(0x74726976)
	virtioMMIOVersion    = uint32(2)          // modern (non-legacy) transport only
	virtioMMIOVendorID   = uint32(0x53434f49) // "SCOI" - this project's own vendor ID; no registry assigns these, any value is valid

	// Register offsets from each device's own base address.
	virtioRegMagicValue        = uint64(0x000)
	virtioRegVersion           = uint64(0x004)
	virtioRegDeviceID          = uint64(0x008)
	virtioRegVendorID          = uint64(0x00c)
	virtioRegDeviceFeatures    = uint64(0x010)
	virtioRegDeviceFeaturesSel = uint64(0x014)
	virtioRegDriverFeatures    = uint64(0x020)
	virtioRegDriverFeaturesSel = uint64(0x024)
	virtioRegQueueSel          = uint64(0x030)
	virtioRegQueueNumMax       = uint64(0x034)
	virtioRegQueueNum          = uint64(0x038)
	virtioRegQueueReady        = uint64(0x044)
	virtioRegQueueNotify       = uint64(0x050)
	virtioRegInterruptStatus   = uint64(0x060)
	virtioRegInterruptACK      = uint64(0x064)
	virtioRegStatus            = uint64(0x070)
	virtioRegQueueDescLow      = uint64(0x080)
	virtioRegQueueDescHigh     = uint64(0x084)
	virtioRegQueueDriverLow    = uint64(0x090)
	virtioRegQueueDriverHigh   = uint64(0x094)
	virtioRegQueueDeviceLow    = uint64(0x0a0)
	virtioRegQueueDeviceHigh   = uint64(0x0a4)
	virtioRegConfigGeneration  = uint64(0x0fc)
	virtioRegConfigSpace       = uint64(0x100)

	// Status register bits (VIRTIO 1.2 section 2.1).
	virtioStatusAcknowledge      = uint32(1)
	virtioStatusDriver           = uint32(2)
	virtioStatusDriverOK         = uint32(4)
	virtioStatusFeaturesOK       = uint32(8)
	virtioStatusDeviceNeedsReset = uint32(64)
	virtioStatusFailed           = uint32(128)

	// InterruptStatus bits (section 4.2.3.10): bit 0 signals used-ring
	// activity, bit 1 a config-space change. This VMM never changes a
	// device's config space after boot, so only bit 0 is ever set.
	virtioInterruptUsedRing = uint32(1)

	// VIRTIO_F_VERSION_1 (bit 32 of the 64-bit feature space): a version-2
	// transport MUST offer it and a compliant driver MUST accept it before
	// setting FEATURES_OK, so it is unconditionally part of every device's
	// offered features below, in addition to whatever device-specific bits
	// each device adds.
	virtioFeatureVersion1 = uint64(1) << 32

	// The maximum single-descriptor-chain-per-request queue depth every
	// device in this VMM uses. There is no meaningful benefit to a deeper
	// queue for a single-vCPU guest talking to an in-process Go device
	// model with no real DMA latency to hide, and a smaller, fixed size
	// keeps the virtqueue math (ring wraparound, bounds checks) simple to
	// get right.
	virtioQueueSize = uint16(256)
)

// virtioMMIODevice is one virtio-mmio device's transport-level state: the
// register file every virtio-mmio device shares, plus a small,
// device-specific interface (readConfig/notify) that plugs in virtio-blk,
// virtio-net, or any future device without duplicating the register/queue
// bookkeeping. It is not safe for concurrent use - every access happens
// from the single vCPU thread inside RunLinuxWithOptions's KVM_RUN loop.
type virtioMMIODevice struct {
	name     string
	base     uint64
	irq      uint32
	deviceID uint32
	features uint64 // device-offered feature bits, always including virtioFeatureVersion1

	driverFeaturesSel uint32
	driverFeatures    uint64 // accumulated as the driver writes each 32-bit half
	deviceFeaturesSel uint32

	status           uint32
	queueSel         uint32
	queues           []virtQueue
	interruptStatus  uint32
	configGeneration uint32

	// configSpace is read-only guest-visible device configuration
	// (virtio-blk's capacity, virtio-net's MAC/status/mtu, ...), laid out
	// exactly as the device-specific struct the guest driver expects,
	// starting at virtioRegConfigSpace.
	configSpace []byte

	// notify is called synchronously, still on the vCPU thread, whenever
	// the driver writes QueueNotify for one of this device's queues. It
	// is where device-specific request processing (virtio-blk I/O,
	// virtio-net packet TX) happens - see linux_virtio_blk.go and
	// linux_virtio_net.go.
	notify func(queueIndex uint32) error

	raiseIRQ func()

	// mu guards every field above plus this device's own writes into
	// guestMemory. Every device takes it, including virtio-blk, which
	// (being driven only by notify calls on the vCPU thread) never
	// actually contends on it - the cost of an uncontended
	// sync.Mutex.Lock/Unlock pair is negligible next to an ioctl-driven
	// KVM_RUN loop, and virtio-net genuinely needs it: its RX path
	// (linux_virtio_net.go's readTAPLoop) runs on its own goroutine,
	// delivering inbound packets into this same device's queues and
	// guest-memory writes concurrently with the vCPU thread's notify
	// calls for outbound (TX) packets.
	mu sync.Mutex

	// guestMemory is the full flat guest-physical-address-space slice
	// RunLinuxWithOptions mmap'd for the whole VM (the same one Linux
	// boot/E820 setup writes into) - set once by RunLinuxWithOptions
	// before the first KVM_RUN, after which every virtqueue address this
	// device's driver programs is resolved against it. It is not this
	// device's own memory; every device sharing one guest shares this
	// same slice.
	guestMemory []byte
}

// newVirtioMMIODevice constructs the shared transport state for a device
// with numQueues virtqueues, each virtioQueueSize deep. deviceFeatures are
// this device's own offered bits (virtioFeatureVersion1 is added
// automatically); configSpace is copied so the caller's own buffer can be
// reused or mutated afterward without aliasing this device's state.
func newVirtioMMIODevice(name string, deviceID uint32, base uint64, irq uint32, numQueues int, deviceFeatures uint64, configSpace []byte) *virtioMMIODevice {
	queues := make([]virtQueue, numQueues)
	for i := range queues {
		queues[i].size = virtioQueueSize
	}
	config := make([]byte, len(configSpace))
	copy(config, configSpace)
	return &virtioMMIODevice{
		name:        name,
		base:        base,
		irq:         irq,
		deviceID:    deviceID,
		features:    deviceFeatures | virtioFeatureVersion1,
		queues:      queues,
		configSpace: config,
	}
}

// virtioMMIOCommandLineParam returns this device's
// `virtio_mmio.device=<size>@<base>:<irq>` kernel command line fragment -
// the only way an x86 guest with no ACPI/devicetree support learns this
// device exists at all (CONFIG_VIRTIO_MMIO_CMDLINE_DEVICES). size is fixed
// at one page: every register this transport implements, including the
// largest config space any device here uses, fits well within it.
func (d *virtioMMIODevice) virtioMMIOCommandLineParam() string {
	return fmt.Sprintf("virtio_mmio.device=%#x@%#x:%d", virtioMMIOWindowSize, d.base, d.irq)
}

// virtioMMIOWindowSize is the guest-physical MMIO window reserved per
// device - one page, matching virtioMMIOCommandLineParam's size field and
// the spacing used to lay out multiple devices (see
// virtioMMIODeviceBaseAddress).
const virtioMMIOWindowSize = uint64(0x1000)

// virtioMMIOBaseAddress is where the first virtio-mmio device's window
// starts in guest physical memory, and virtioMMIODeviceBaseAddress derives
// every subsequent device's window from it. This address is deliberately
// far above any guest memory size this VMM configures (see
// RunLinuxWithOptions's own memory size validation) - a guest access here
// always misses the single low-memory KVM_SET_USER_MEMORY_REGION slot and
// therefore always traps as KVM_EXIT_MMIO, which is what makes this a
// trappable MMIO region at all rather than ordinary backed RAM. The same
// convention (a fixed high MMIO base, one page per device) is used by
// other minimal VMMs (Firecracker, crosvm) for the same reason.
const virtioMMIOBaseAddress = uint64(0xd000_0000)

func virtioMMIODeviceBaseAddress(index int) uint64 {
	return virtioMMIOBaseAddress + uint64(index)*virtioMMIOWindowSize
}

// inWindow reports whether addr falls inside this device's MMIO window.
func (d *virtioMMIODevice) inWindow(addr uint64) bool {
	return addr >= d.base && addr < d.base+virtioMMIOWindowSize
}

// handle services one KVM_EXIT_MMIO access already confirmed to fall
// inside this device's window. data is the guest-supplied write value (for
// isWrite) or the buffer to fill (for a read), always 1/2/4/8 bytes as KVM
// itself already validated before this VMM ever sees the exit.
func (d *virtioMMIODevice) handle(addr uint64, isWrite bool, data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.handleLocked(addr, isWrite, data)
}

// withLock runs fn with this device's lock held and guestMemory available,
// for a caller outside the vCPU thread's MMIO dispatch path - virtio-net's
// readTAPLoop goroutine is the only one today (see linux_virtio_net.go).
func (d *virtioMMIODevice) withLock(fn func(mem []byte)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fn(d.guestMemory)
}

func (d *virtioMMIODevice) handleLocked(addr uint64, isWrite bool, data []byte) error {
	offset := addr - d.base
	if offset >= virtioRegConfigSpace {
		return d.handleConfigSpace(offset-virtioRegConfigSpace, isWrite, data)
	}
	if isWrite {
		return d.handleRegisterWrite(offset, data)
	}
	return d.handleRegisterRead(offset, data)
}

func (d *virtioMMIODevice) handleConfigSpace(offset uint64, isWrite bool, data []byte) error {
	if isWrite {
		// Every device this VMM implements has a read-only config space
		// (virtio-blk's capacity, virtio-net's MAC/status/mtu are all
		// fixed at device-creation time, never negotiated or reconfigured
		// by the driver) - writes are simply discarded, matching how real
		// read-only virtio config fields behave.
		return nil
	}
	for i := range data {
		pos := offset + uint64(i)
		if pos < uint64(len(d.configSpace)) {
			data[i] = d.configSpace[pos]
		} else {
			data[i] = 0
		}
	}
	return nil
}

func (d *virtioMMIODevice) handleRegisterRead(offset uint64, data []byte) error {
	var value uint32
	switch offset {
	case virtioRegMagicValue:
		value = virtioMMIOMagicValue
	case virtioRegVersion:
		value = virtioMMIOVersion
	case virtioRegDeviceID:
		value = d.deviceID
	case virtioRegVendorID:
		value = virtioMMIOVendorID
	case virtioRegDeviceFeatures:
		if d.deviceFeaturesSel == 1 {
			value = uint32(d.features >> 32)
		} else {
			value = uint32(d.features)
		}
	case virtioRegQueueNumMax:
		value = uint32(virtioQueueSize)
	case virtioRegQueueReady:
		if q := d.selectedQueue(); q != nil && q.ready {
			value = 1
		}
	case virtioRegInterruptStatus:
		value = d.interruptStatus
	case virtioRegStatus:
		value = d.status
	case virtioRegConfigGeneration:
		value = d.configGeneration
	default:
		value = 0
	}
	putRegisterBytes(data, value)
	return nil
}

func (d *virtioMMIODevice) handleRegisterWrite(offset uint64, data []byte) error {
	value := registerBytesValue(data)
	switch offset {
	case virtioRegDeviceFeaturesSel:
		d.deviceFeaturesSel = value
	case virtioRegDriverFeatures:
		if d.driverFeaturesSel == 1 {
			d.driverFeatures = d.driverFeatures&0x0000_0000_ffff_ffff | uint64(value)<<32
		} else {
			d.driverFeatures = d.driverFeatures&0xffff_ffff_0000_0000 | uint64(value)
		}
	case virtioRegDriverFeaturesSel:
		d.driverFeaturesSel = value
	case virtioRegQueueSel:
		d.queueSel = value
	case virtioRegQueueNum:
		if q := d.selectedQueue(); q != nil && value > 0 && value <= uint32(virtioQueueSize) {
			q.size = uint16(value)
		}
	case virtioRegQueueReady:
		if q := d.selectedQueue(); q != nil {
			q.ready = value != 0
		}
	case virtioRegQueueNotify:
		if d.notify != nil {
			return d.notify(value)
		}
	case virtioRegInterruptACK:
		d.interruptStatus &^= value
	case virtioRegStatus:
		if value == 0 {
			d.reset()
		} else {
			d.status = value
		}
	case virtioRegQueueDescLow:
		if q := d.selectedQueue(); q != nil {
			q.descAddr = q.descAddr&0xffff_ffff_0000_0000 | uint64(value)
		}
	case virtioRegQueueDescHigh:
		if q := d.selectedQueue(); q != nil {
			q.descAddr = q.descAddr&0x0000_0000_ffff_ffff | uint64(value)<<32
		}
	case virtioRegQueueDriverLow:
		if q := d.selectedQueue(); q != nil {
			q.availAddr = q.availAddr&0xffff_ffff_0000_0000 | uint64(value)
		}
	case virtioRegQueueDriverHigh:
		if q := d.selectedQueue(); q != nil {
			q.availAddr = q.availAddr&0x0000_0000_ffff_ffff | uint64(value)<<32
		}
	case virtioRegQueueDeviceLow:
		if q := d.selectedQueue(); q != nil {
			q.usedAddr = q.usedAddr&0xffff_ffff_0000_0000 | uint64(value)
		}
	case virtioRegQueueDeviceHigh:
		if q := d.selectedQueue(); q != nil {
			q.usedAddr = q.usedAddr&0x0000_0000_ffff_ffff | uint64(value)<<32
		}
	}
	return nil
}

// selectedQueue returns the queue QueueSel currently names, or nil if out
// of range - a driver is free to probe QueueSel values past this device's
// real queue count while enumerating, and every register access above
// that touches "the selected queue" must tolerate that rather than panic.
func (d *virtioMMIODevice) selectedQueue() *virtQueue {
	if int(d.queueSel) >= len(d.queues) {
		return nil
	}
	return &d.queues[d.queueSel]
}

// reset restores every negotiable and per-queue field to its power-on
// state, matching what a driver-initiated Status=0 write (or this VMM's
// own boot-time initialization) must produce (VIRTIO 1.2 section 2.1.1,
// "device reset").
func (d *virtioMMIODevice) reset() {
	d.status = 0
	d.driverFeatures = 0
	d.driverFeaturesSel = 0
	d.deviceFeaturesSel = 0
	d.queueSel = 0
	d.interruptStatus = 0
	for i := range d.queues {
		d.queues[i] = virtQueue{size: virtioQueueSize}
	}
}

// queueReadyForIO reports whether queueIndex is a valid, driver-activated
// queue this device can process requests from - the common precondition
// every device's notify callback checks before touching the ring.
func (d *virtioMMIODevice) queueReadyForIO(queueIndex uint32) (*virtQueue, bool) {
	if d.status&virtioStatusDriverOK == 0 || int(queueIndex) >= len(d.queues) {
		return nil, false
	}
	q := &d.queues[queueIndex]
	if !q.ready {
		return nil, false
	}
	return q, true
}

// signalUsedRing sets the used-ring interrupt status bit and raises this
// device's IRQ - called by a device's notify callback (or, for
// host-initiated activity like an inbound virtio-net packet, from outside
// the notify path entirely) after pushUsed has made new completions
// visible in guest memory.
func (d *virtioMMIODevice) signalUsedRing() {
	d.interruptStatus |= virtioInterruptUsedRing
	if d.raiseIRQ != nil {
		d.raiseIRQ()
	}
}

func putRegisterBytes(data []byte, value uint32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], value)
	copy(data, buf[:len(data)])
}

func registerBytesValue(data []byte) uint32 {
	var buf [4]byte
	copy(buf[:], data)
	return binary.LittleEndian.Uint32(buf[:])
}

// handleLinuxVirtioMMIO dispatches a KVM_EXIT_MMIO access to whichever
// registered device's window contains addr, if any. It returns handled=false
// (not an error) for an address inside no device's window: guest firmware-
// probing code occasionally reads addresses speculatively, and the E820 map
// never claims this high MMIO region as RAM in the first place, so an
// unclaimed access here is not itself a sign of anything wrong - see
// handleLinuxPortIO's analogous devices for the same "false, nil" contract.
func handleLinuxVirtioMMIO(devices []*virtioMMIODevice, addr uint64, isWrite bool, data []byte) (bool, error) {
	for _, device := range devices {
		if !device.inWindow(addr) {
			continue
		}
		if debugVirtioMMIO {
			traceVirtioMMIOAccess(device, addr, isWrite, data)
		}
		err := device.handle(addr, isWrite, data)
		if debugVirtioMMIO && !isWrite {
			fmt.Fprintf(os.Stderr, "  -> result=%x\n", data)
		}
		return true, err
	}
	return false, nil
}

// debugVirtioMMIO enables a raw register-access trace on stderr - not
// guest-visible, entirely host-side - covering every read/write this
// transport ever handles across every registered device: magic/feature
// negotiation, per-queue descriptor/avail/used addresses, status
// transitions, config space bytes, all in the exact order the guest driver
// issued them.
var debugVirtioMMIO = os.Getenv("PLATFORM_FACTORY_DEBUG_VIRTIO_MMIO") == "1"

func traceVirtioMMIOAccess(device *virtioMMIODevice, addr uint64, isWrite bool, data []byte) {
	direction := "READ "
	if isWrite {
		direction = "WRITE"
	}
	fmt.Fprintf(os.Stderr, "virtio-mmio[%s] %s addr=%#x offset=%#x data=%x\n", device.name, direction, addr, addr-device.base, data)
}

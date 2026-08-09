//go:build linux && amd64

package kvm

import (
	"io"
	"net"
	"os"
)

// BlockBackend is the storage backing a virtio-blk device. *os.File
// satisfies it directly; anything else needs only these three methods.
type BlockBackend interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
}

// BlockDeviceOptions configures one virtio-blk device visible to the guest
// as /dev/vda, /dev/vdb, ... in the order they appear in
// LinuxRunOptions.BlockDevices.
type BlockDeviceOptions struct {
	// Backend is the file (or anything ReadAt/WriteAt/Sync-capable) the
	// device reads and writes. RunLinuxWithOptions never opens, closes,
	// or truncates it - the caller owns its whole lifecycle.
	Backend BlockBackend
	// Capacity is the device's size in bytes, reported to the guest via
	// virtio-blk's config space. It must be a positive multiple of 512
	// (VIRTIO_BLK's sector size) and callers are responsible for it
	// actually matching what Backend can serve - this VMM does not stat
	// the backend to double-check, since not every BlockBackend
	// implementation can be stat'd (a raw io.ReaderAt/WriterAt pair over
	// something that isn't a real file, for instance).
	Capacity uint64
	// ReadOnly rejects every VIRTIO_BLK_T_OUT (write) request with
	// VIRTIO_BLK_S_IOERR and marks the device read-only in its config
	// space, without ever calling Backend.WriteAt.
	ReadOnly bool
}

// NetworkDeviceOptions configures one virtio-net device backed by an
// already-open, already-configured TAP file descriptor - see OpenTAP.
type NetworkDeviceOptions struct {
	// TAP is an IFF_TAP|IFF_NO_PI descriptor already attached to a host
	// network interface (OpenTAP returns one). RunLinuxWithOptions reads
	// and writes raw Ethernet frames directly on it and closes it when
	// the guest stops running, whether or not that guest ever cleanly
	// shuts down.
	TAP *os.File
	// MAC is the guest-visible virtio-net device's own MAC address,
	// reported through virtio-net's config space. It has no relationship
	// to the TAP interface's own host-side MAC.
	MAC net.HardwareAddr
}

// virtioFirstDeviceIRQ is the first GSI available for a virtio-mmio device:
// IRQ3/IRQ4 are COM2 (the authenticated guest-transport channel) and COM1
// (boot diagnostics) respectively, already permanently claimed by
// RunLinuxWithOptions itself (see its raiseSerialIRQ/GuestChannel wiring).
// Every other legacy PC IRQ this range skips (0 PIT, 1 keyboard, 2 PIC
// cascade, 8 RTC, 12 PS/2 mouse, 13 FPU) has no emulated device behind it
// in this VMM at all - nothing ever raises them - so reusing their GSI
// numbers here would be safe too, but starting past the ones this VMM's
// own fixed hardware model document (linux_i8042.go, linux_serial.go)
// actually names keeps every claimed GSI traceable to a real, present
// device.
const virtioFirstDeviceIRQ = uint32(5)

// buildVirtioDevices constructs the virtio-mmio transport for every
// configured block/network device (block devices first, matching the
// guest-visible /dev/vda, /dev/vdb, ... ordering VIRTIO_BLK_F_* naming
// implies), and returns the kernel command line with each device's
// `virtio_mmio.device=` fragment appended - the only way this VMM's
// PCI-less, ACPI-less guest ever learns these devices exist (see
// linux_virtio_mmio.go's own doc comment). The returned stop functions
// must be called (LinuxRunOptions callers do this via defer) before the
// guest memory and TAP descriptors they close over are torn down.
func buildVirtioDevices(options LinuxRunOptions, commandLine string) (devices []*virtioMMIODevice, stops []func(), augmentedCommandLine string, err error) {
	index := 0
	irq := virtioFirstDeviceIRQ
	var params []string
	cleanup := func() {
		for _, stop := range stops {
			stop()
		}
	}
	for _, blkOpts := range options.BlockDevices {
		device, err := newVirtioBlkMMIODevice(blkOpts, virtioMMIODeviceBaseAddress(index), irq)
		if err != nil {
			cleanup()
			return nil, nil, "", err
		}
		devices = append(devices, device)
		params = append(params, device.virtioMMIOCommandLineParam())
		index++
		irq++
	}
	for _, netOpts := range options.NetworkDevices {
		device, stop, err := newVirtioNetMMIODevice(netOpts, virtioMMIODeviceBaseAddress(index), irq)
		if err != nil {
			cleanup()
			return nil, nil, "", err
		}
		devices = append(devices, device)
		stops = append(stops, stop)
		params = append(params, device.virtioMMIOCommandLineParam())
		index++
		irq++
	}
	augmentedCommandLine = commandLine
	for _, p := range params {
		augmentedCommandLine += " " + p
	}
	return devices, stops, augmentedCommandLine, nil
}

//go:build linux && amd64

package kvm

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	kvmIoctlSetTSSAddress     = uintptr(0xae47)
	kvmIoctlCreateIRQChip     = uintptr(0xae60)
	kvmIoctlCreatePIT2        = uintptr(0x4040ae77)
	kvmIoctlGetSupportedCPUID = uintptr(0xc008ae05)
	kvmIoctlSetCPUID2         = uintptr(0x4008ae90)
	kvmIoctlGetTSCKHZ         = uintptr(0xaea3)
	kvmExitShutdown           = uint32(8)
	kvmExitInternalError      = uint32(17)
	kvmExitSystemEvent        = uint32(24)
	kvmTSSAddress             = uintptr(0xfffb_d000)
	kvmRunIOCountOffset       = 36
	// KVM_PIT_SPEAKER_DUMMY does not mean "fake" despite the name: it tells
	// KVM_CREATE_PIT2 to also register its own in-kernel handler for port
	// 0x61 (the PC speaker/gate port, KVM_SPEAKER_BASE_ADDRESS), backed by
	// the same real, elapsed-time-accurate PIT channel-2 countdown as
	// ports 0x40-0x43. Without it, port 0x61 I/O still exits to
	// userspace with no in-kernel device behind it at all - see
	// configureSupportedCPUID's doc comment for what that costs a guest
	// that needs it for TSC calibration.
	kvmPITSpeakerDummy = uint32(1)
)

type kvmPITConfig struct {
	Flags uint32
	Pad   [15]uint32
}

// LinuxRunOptions configures optional lifecycle notifications for RunLinux.
type LinuxRunOptions struct {
	// OnStarted is called exactly once, after the first KVM_RUN ioctl
	// completes successfully. It is never called when setup fails or every
	// KVM_RUN attempt fails. A callback error stops the VM immediately.
	OnStarted func() error

	// GuestChannel exposes a dedicated bidirectional 8250 UART at COM2
	// (I/O 0x2f8, IRQ3; normally /dev/ttyS1 in Linux). RunLinux owns and
	// closes the stream. COM1 remains reserved for boot diagnostics.
	GuestChannel io.ReadWriteCloser

	// SerialWriter receives COM1 bytes as the guest emits them. The same
	// bytes remain available in LinuxRunResult.Serial, subject to its
	// bounded capture limit. A writer error stops the VM.
	SerialWriter io.Writer

	// BlockDevices configures zero or more virtio-blk devices, visible to
	// the guest in this order as /dev/vda, /dev/vdb, .... See
	// BlockDeviceOptions.
	BlockDevices []BlockDeviceOptions

	// NetworkDevices configures zero or more virtio-net devices, each
	// backed by an already-open TAP descriptor. See NetworkDeviceOptions.
	NetworkDevices []NetworkDeviceOptions
}

type linuxRunStartNotifier struct {
	callback func() error
	notified bool
}

func (n *linuxRunStartNotifier) notify() error {
	if n.notified {
		return nil
	}
	n.notified = true
	if n.callback != nil {
		return n.callback()
	}
	return nil
}

// RunLinux creates a native single-vCPU KVM machine, loads a Linux bzImage,
// initrd and command line, configures protected-mode CPU state, irqchip/PIT and
// a minimal COM1 data plane, then enters KVM_RUN. It never invokes QEMU,
// firmware, a shell, or another VMM.
func RunLinux(ctx context.Context, memoryBytes uint64, kernel, initrd []byte, commandLine string, maxExits int) (LinuxRunResult, error) {
	return RunLinuxWithOptions(ctx, memoryBytes, kernel, initrd, commandLine, maxExits, LinuxRunOptions{})
}

// RunLinuxWithOptions is RunLinux with optional lifecycle notifications.
func RunLinuxWithOptions(ctx context.Context, memoryBytes uint64, kernel, initrd []byte, commandLine string, maxExits int, options LinuxRunOptions) (LinuxRunResult, error) {
	var result LinuxRunResult
	if options.GuestChannel != nil {
		defer options.GuestChannel.Close()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	maxInt := uint64(^uint(0) >> 1)
	if memoryBytes < 16<<20 || memoryBytes > maxInt || memoryBytes%uint64(os.Getpagesize()) != 0 {
		return result, fmt.Errorf("vmm: kvm: linux boot: memory size %d must be page-aligned and at least 16 MiB", memoryBytes)
	}
	if maxExits <= 0 {
		return result, fmt.Errorf("vmm: kvm: linux boot: max exits must be greater than zero")
	}
	virtioDevices, virtioStops, commandLine, err := buildVirtioDevices(options, commandLine)
	if err != nil {
		return result, err
	}
	defer func() {
		for _, stop := range virtioStops {
			stop()
		}
	}()
	memory, err := syscall.Mmap(-1, 0, int(memoryBytes), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS)
	if err != nil {
		return result, fmt.Errorf("vmm: kvm: mmap guest memory: %w", err)
	}
	defer syscall.Munmap(memory)
	for _, device := range virtioDevices {
		device.guestMemory = memory
	}
	layout, err := loadLinuxBootWithEntropyReader(memory, kernel, initrd, commandLine, rand.Reader)
	if err != nil {
		return result, err
	}

	kvmFile, err := os.OpenFile("/dev/kvm", os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return result, fmt.Errorf("vmm: kvm: open /dev/kvm: %w", err)
	}
	defer kvmFile.Close()
	if err := negotiateRequiredKVMExtensions(kvmExtensionChecker(kvmFile), map[string]bool{}, map[string]string{}); err != nil {
		return result, fmt.Errorf("vmm: kvm: required extensions: %w", err)
	}
	vmFD, err := ioctlNoArg(kvmFile.Fd(), kvmIoctlCreateVM)
	if err != nil {
		return result, fmt.Errorf("vmm: kvm: KVM_CREATE_VM: %w", err)
	}
	vmFile := os.NewFile(vmFD, "kvm-linux-vm")
	defer vmFile.Close()

	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, vmFile.Fd(), kvmIoctlSetTSSAddress, kvmTSSAddress); errno != 0 {
		return result, fmt.Errorf("vmm: kvm: KVM_SET_TSS_ADDR: %w", errno)
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, vmFile.Fd(), kvmIoctlCreateIRQChip, 0); errno != 0 {
		return result, fmt.Errorf("vmm: kvm: KVM_CREATE_IRQCHIP: %w", errno)
	}
	pit := kvmPITConfig{Flags: kvmPITSpeakerDummy}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, vmFile.Fd(), kvmIoctlCreatePIT2, uintptr(unsafe.Pointer(&pit))); errno != 0 {
		return result, fmt.Errorf("vmm: kvm: KVM_CREATE_PIT2: %w", errno)
	}

	region := kvmUserspaceMemoryRegion{
		Slot: 0, GuestPhysAddr: 0, MemorySize: memoryBytes,
		UserspaceAddr: uint64(uintptr(unsafe.Pointer(&memory[0]))),
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, vmFile.Fd(), kvmIoctlSetUserMemRegion, uintptr(unsafe.Pointer(&region))); errno != 0 {
		return result, fmt.Errorf("vmm: kvm: KVM_SET_USER_MEMORY_REGION: %w", errno)
	}
	vcpuFD, err := ioctlNoArg(vmFile.Fd(), kvmIoctlCreateVCPU)
	if err != nil {
		return result, fmt.Errorf("vmm: kvm: KVM_CREATE_VCPU: %w", err)
	}
	vcpuFile := os.NewFile(vcpuFD, "kvm-linux-vcpu")
	defer vcpuFile.Close()
	if err := configureSupportedCPUID(kvmFile, vcpuFile); err != nil {
		return result, err
	}
	if err := configureLinuxBootVCPU(vcpuFile, memory, layout); err != nil {
		return result, err
	}

	mmapSize, err := ioctlNoArg(kvmFile.Fd(), kvmIoctlGetVCPUMmapSize)
	if err != nil {
		return result, fmt.Errorf("vmm: kvm: KVM_GET_VCPU_MMAP_SIZE: %w", err)
	}
	runRegion, err := syscall.Mmap(int(vcpuFile.Fd()), 0, int(mmapSize), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return result, fmt.Errorf("vmm: kvm: mmap kvm_run: %w", err)
	}
	defer syscall.Munmap(runRegion)
	if len(runRegion) <= kvmExitIODataOffsetOffset+8 {
		return result, fmt.Errorf("vmm: kvm: kvm_run mapping is unexpectedly small: %d", len(runRegion))
	}

	runControl := (*uint32)(unsafe.Pointer(&runRegion[0]))
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	threadID := syscall.Gettid()
	stopInterrupt := make(chan struct{})
	interruptDone := make(chan struct{})
	go interruptKVMRun(ctx, runControl, threadID, stopInterrupt, interruptDone)
	defer func() {
		close(stopInterrupt)
		<-interruptDone
	}()

	serialState := linuxSerialState{output: options.SerialWriter}
	raiseIRQ := func(irq uint32) {
		level := kvmIRQLevel{IRQ: irq, Level: 1}
		syscall.Syscall(syscall.SYS_IOCTL, vmFile.Fd(), kvmIoctlIRQLine, uintptr(unsafe.Pointer(&level)))
		level.Level = 0
		syscall.Syscall(syscall.SYS_IOCTL, vmFile.Fd(), kvmIoctlIRQLine, uintptr(unsafe.Pointer(&level)))
	}
	raiseSerialIRQ := func() { raiseIRQ(4) }
	for _, device := range virtioDevices {
		irq := device.irq
		device.raiseIRQ = func() { raiseIRQ(irq) }
	}
	var guestChannel *linuxGuestSerialDevice
	if options.GuestChannel != nil {
		guestChannel = newLinuxGuestSerialDevice(options.GuestChannel, func() { raiseIRQ(3) })
		defer guestChannel.Close()
	}
	startNotifier := linuxRunStartNotifier{callback: options.OnStarted}
	for result.Exits < maxExits {
		if guestChannel != nil {
			if err := guestChannel.transportError(); err != nil {
				return result, err
			}
		}
		setKVMImmediateExit(runControl, false)
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, vcpuFile.Fd(), kvmIoctlRun, 0); errno != 0 {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			// Go uses asynchronous signals for runtime preemption. KVM_RUN
			// may consequently return EINTR even though neither the caller
			// nor the guest requested a stop. No VM exit was delivered in
			// that case, so retry without consuming the exit budget.
			if errno == syscall.EINTR {
				continue
			}
			return result, fmt.Errorf("vmm: kvm: KVM_RUN: %w", errno)
		}
		if err := startNotifier.notify(); err != nil {
			return result, fmt.Errorf("vmm: kvm: publish Linux guest start: %w", err)
		}
		result.Exits++
		result.ExitReason = *(*uint32)(unsafe.Pointer(&runRegion[kvmExitReasonOffset]))
		switch result.ExitReason {
		case kvmExitIO:
			if err := handleLinuxPortIO(runRegion, &serialState, guestChannel, &result, raiseSerialIRQ, func() { raiseIRQ(3) }); err != nil {
				return result, err
			}
		case kvmExitMMIO:
			if err := handleLinuxMMIOExit(runRegion, virtioDevices); err != nil {
				return result, err
			}
		case kvmExitHLT:
			result.Halted = true
			return result, nil
		case kvmExitShutdown:
			result.Shutdown = true
			return result, nil
		case kvmExitInternalError, kvmExitSystemEvent:
			return result, fmt.Errorf("vmm: kvm: guest terminated with exit reason %d", result.ExitReason)
		default:
			return result, fmt.Errorf("vmm: kvm: unhandled Linux guest exit reason %d", result.ExitReason)
		}
	}
	return result, fmt.Errorf("vmm: kvm: Linux guest exceeded %d exits", maxExits)
}

func loadLinuxBootWithEntropyReader(memory, kernel, initrd []byte, commandLine string, source io.Reader) (LinuxBootLayout, error) {
	const seedBytes = 64
	seed := make([]byte, seedBytes)
	defer clear(seed)
	if _, err := io.ReadFull(source, seed); err != nil {
		return LinuxBootLayout{}, fmt.Errorf("vmm: kvm: read guest entropy seed: %w", err)
	}
	return LoadLinuxBootWithEntropy(memory, kernel, initrd, commandLine, seed)
}

// configureSupportedCPUID gives the guest the host features KVM explicitly
// supports. A newly-created vCPU otherwise has an empty/default CPUID model;
// that is enough for tiny flat payloads but not for an x86_64 Linux
// decompressor, which must discover long mode and its required CPU features.
func configureSupportedCPUID(kvmFile, vcpuFile *os.File) error {
	const (
		cpuidEntries    = 256
		cpuidHeaderSize = 8
		cpuidEntrySize  = 40
	)
	cpuid := make([]byte, cpuidHeaderSize+cpuidEntries*cpuidEntrySize)
	*(*uint32)(unsafe.Pointer(&cpuid[0])) = cpuidEntries
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, kvmFile.Fd(),
		kvmIoctlGetSupportedCPUID, uintptr(unsafe.Pointer(&cpuid[0]))); errno != 0 {
		return fmt.Errorf("vmm: kvm: KVM_GET_SUPPORTED_CPUID: %w", errno)
	}
	entries := *(*uint32)(unsafe.Pointer(&cpuid[0]))
	if entries == 0 || entries > cpuidEntries {
		return fmt.Errorf("vmm: kvm: invalid supported CPUID entry count %d", entries)
	}
	// KVM deliberately reports crystal-clock CPUID leaf 0x15 (and 0x16) as
	// all-zero from KVM_GET_SUPPORTED_CPUID: guest TSC frequency is a VMM
	// policy decision (host TSC passthrough here, no scaling), not
	// something KVM can assume on the guest's behalf. Populating leaf
	// 0x15 with the real, KVM-reported TSC frequency lets an Intel guest
	// calibrate directly from a single CPUID read.
	//
	// That only covers Intel, though: Linux's native_calibrate_tsc() and
	// cpu_khz_from_cpuid() both bail out immediately on any other vendor
	// (arch/x86/kernel/tsc.c), leaf 0x15 or not. An AMD guest always
	// falls through to pit_hpet_ptimer_calibrate_cpu(), which - lacking
	// HPET/PMTIMER in this minimal boot environment - depends entirely on
	// a working PIT channel-2/port-0x61 gate readback to calibrate
	// against. KVM_CREATE_PIT2's kvmPITSpeakerDummy flag (see its own doc
	// comment) is what makes that readback real instead of a permanent
	// exit to a userspace handler with nothing behind it.
	if tscKHz, err := ioctlNoArg(vcpuFile.Fd(), kvmIoctlGetTSCKHZ); err == nil && tscKHz > 0 && tscKHz <= 0x7fffffff {
		entries = injectTSCFrequencyLeaf(cpuid, entries, uint32(tscKHz))
	}
	// Hosts vary in which KVM paravirtualized-guest CPUID leaves they
	// advertise as supported (this depends on the host kernel/KVM module
	// version, not anything guest-visible in this VMM's own behavior).
	// Left exposed, Linux's init_hypervisor_platform() detects "Hypervisor:
	// KVM" and calls kvm_guest_init(), opting the guest into paravirt
	// protocols - PV EOI, kvmclock, async page faults, PV TLB flush/spinlock
	// kicks - that assume a full KVM host-side implementation of their
	// MSR-based handshakes. This VMM only emulates the fixed, minimal
	// hardware model documented at the top of this file; it implements none
	// of them. Hiding CPUID.1:ECX.31 (the hypervisor-present bit) and the
	// entire 0x40000000-0x400000FF leaf range makes every host boot the
	// guest the same, well-exercised "bare hardware" way regardless of what
	// paravirt features the underlying host KVM happens to expose.
	const (
		hypervisorPresentBit = uint32(1) << 31
		kvmLeafRangeStart    = uint32(0x40000000)
		kvmLeafRangeEnd      = uint32(0x400000ff)
	)
	for i := uint32(0); i < entries; i++ {
		base := cpuidHeaderSize + int(i)*cpuidEntrySize
		function := *(*uint32)(unsafe.Pointer(&cpuid[base]))
		switch {
		case function == 1:
			ecx := (*uint32)(unsafe.Pointer(&cpuid[base+20]))
			*ecx &^= hypervisorPresentBit
		case function >= kvmLeafRangeStart && function <= kvmLeafRangeEnd:
			for _, offset := range [4]int{12, 16, 20, 24} {
				*(*uint32)(unsafe.Pointer(&cpuid[base+offset])) = 0
			}
		}
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, vcpuFile.Fd(),
		kvmIoctlSetCPUID2, uintptr(unsafe.Pointer(&cpuid[0]))); errno != 0 {
		return fmt.Errorf("vmm: kvm: KVM_SET_CPUID2: %w", errno)
	}
	return nil
}

// injectTSCFrequencyLeaf writes tscKHz into cpuid's crystal-clock leaf
// (0x15), editing an existing entry if the host's KVM_GET_SUPPORTED_CPUID
// response already contains one, or appending a fresh one otherwise, and
// returns the (possibly incremented) entry count. cpuid must have room for
// one more entry beyond entries; the caller sizes it at cpuidEntries
// (currently 256), far more than any real host reports.
//
// Directed leaf-0x15 passthrough is a comparatively recent KVM feature, so
// older host kernel/KVM module combinations never list a 0x15 entry in
// KVM_GET_SUPPORTED_CPUID at all. Only ever editing an existing entry left
// those hosts with no leaf 0x15 in the guest's CPUID whatsoever - silently
// undoing the fix this function exists for, and putting the guest right
// back on the PIT/HPET calibration path documented on this function's
// caller.
func injectTSCFrequencyLeaf(cpuid []byte, entries, tscKHz uint32) uint32 {
	const (
		cpuidHeaderSize = 8
		cpuidEntrySize  = 40
	)
	for i := uint32(0); i < entries; i++ {
		base := cpuidHeaderSize + int(i)*cpuidEntrySize
		if *(*uint32)(unsafe.Pointer(&cpuid[base])) != 0x15 {
			continue
		}
		*(*uint32)(unsafe.Pointer(&cpuid[base+12])) = 1             // eax: denominator
		*(*uint32)(unsafe.Pointer(&cpuid[base+16])) = 1             // ebx: numerator
		*(*uint32)(unsafe.Pointer(&cpuid[base+20])) = tscKHz * 1000 // ecx: crystal Hz
		*(*uint32)(unsafe.Pointer(&cpuid[base+24])) = 0             // edx
		return entries
	}
	base := cpuidHeaderSize + int(entries)*cpuidEntrySize
	*(*uint32)(unsafe.Pointer(&cpuid[base])) = 0x15             // function
	*(*uint32)(unsafe.Pointer(&cpuid[base+12])) = 1             // eax: denominator
	*(*uint32)(unsafe.Pointer(&cpuid[base+16])) = 1             // ebx: numerator
	*(*uint32)(unsafe.Pointer(&cpuid[base+20])) = tscKHz * 1000 // ecx: crystal Hz
	entries++
	*(*uint32)(unsafe.Pointer(&cpuid[0])) = entries
	return entries
}

func interruptKVMRun(ctx context.Context, control *uint32, threadID int, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	select {
	case <-ctx.Done():
		setKVMImmediateExit(control, true)
		_, _, _ = syscall.RawSyscall6(syscall.SYS_TGKILL, uintptr(os.Getpid()), uintptr(threadID), uintptr(syscall.SIGURG), 0, 0, 0)
	case <-stop:
	}
}

// handleLinuxMMIOExit services a KVM_EXIT_MMIO by dispatching to whichever
// registered virtio-mmio device's window the guest's access falls inside,
// mirroring handleLinuxPortIO's I/O-exit handling for port-mapped devices.
// An access inside no device's window is not an error - see
// handleLinuxVirtioMMIO's own doc comment - but KVM_SET_USER_MEMORY_REGION
// covers only [0, memoryBytes) starting at guest-physical 0, so in
// practice the only way to reach KVM_EXIT_MMIO at all is a genuine access
// to a virtio-mmio device's own high MMIO window (see
// virtioMMIOBaseAddress) or, in principle, an out-of-bounds guest access
// this VMM never intentionally causes.
func handleLinuxMMIOExit(runRegion []byte, devices []*virtioMMIODevice) error {
	const (
		physAddrOffset = kvmExitMMIOPhysAddrOffset
		dataOffset     = kvmExitMMIODataOffset
		lenOffset      = kvmExitMMIOLenOffset
		isWriteOffset  = kvmExitMMIOIsWriteOffset
	)
	if len(runRegion) <= isWriteOffset {
		return fmt.Errorf("vmm: kvm: kvm_run mapping is too small for an MMIO exit")
	}
	physAddr := *(*uint64)(unsafe.Pointer(&runRegion[physAddrOffset]))
	length := *(*uint32)(unsafe.Pointer(&runRegion[lenOffset]))
	isWrite := runRegion[isWriteOffset] != 0
	if length == 0 || length > 8 {
		return fmt.Errorf("vmm: kvm: MMIO access length %d is outside the 1-8 byte range struct kvm_run.mmio.data can hold", length)
	}
	data := runRegion[dataOffset : dataOffset+uint64(length)]
	if _, err := handleLinuxVirtioMMIO(devices, physAddr, isWrite, data); err != nil {
		return fmt.Errorf("vmm: kvm: virtio-mmio access at %#x: %w", physAddr, err)
	}
	return nil
}

func handleLinuxPortIO(runRegion []byte, serialState *linuxSerialState, guestChannel *linuxGuestSerialDevice, result *LinuxRunResult, raiseIRQ, raiseGuestIRQ func()) error {
	direction := runRegion[kvmExitIODirectionOffset]
	size := uint64(runRegion[kvmExitIOSizeOffset])
	port := *(*uint16)(unsafe.Pointer(&runRegion[kvmExitIOPortOffset]))
	count := uint64(*(*uint32)(unsafe.Pointer(&runRegion[kvmRunIOCountOffset])))
	offset := *(*uint64)(unsafe.Pointer(&runRegion[kvmExitIODataOffsetOffset]))
	if size == 0 || count == 0 || count > (^uint64(0))/size {
		return fmt.Errorf("vmm: kvm: invalid I/O size/count %d/%d", size, count)
	}
	length := size * count
	if offset > uint64(len(runRegion)) || length > uint64(len(runRegion))-offset {
		return fmt.Errorf("vmm: kvm: I/O range offset=%d size=%d exceeds kvm_run mapping", offset, length)
	}
	data := runRegion[offset : offset+length]

	if handled, err := handleLinuxI8042IO(direction, size, port, data); handled {
		return err
	}
	if handled, err := handleLinuxPCIConfigIO(direction, size, port, data); handled {
		return err
	}
	if guestChannel != nil {
		if handled, err := guestChannel.handle(direction, size, port, data, raiseGuestIRQ); handled {
			return err
		}
	} else if handled, err := handleLinuxAbsentGuestChannelIO(direction, size, port, data); handled {
		return err
	}
	handleLinuxSerialIO(direction, size, port, count, data, serialState, result, raiseIRQ)
	if serialState.outputErr != nil {
		return fmt.Errorf("vmm: kvm: stream Linux guest serial output: %w", serialState.outputErr)
	}
	return nil
}

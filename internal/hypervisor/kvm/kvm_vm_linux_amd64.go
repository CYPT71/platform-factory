//go:build linux && amd64

package kvm

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// KVM ioctl numbers and struct layouts below are not hand-derived: every
// one was confirmed against the real Linux x86_64 uapi kernel header
// (linux/kvm.h, asm/kvm.h) via compile-time _Static_assert against
// linux-libc-dev's actual struct definitions and macro expansions, cross-
// compiled for x86_64. A wrong constant here would silently misinterpret
// guest memory or fail an ioctl outright, so no value in this file is a
// remembered/guessed magic number.
const (
	kvmIoctlCreateVM          = uintptr(0xae01)
	kvmIoctlCreateVCPU        = uintptr(0xae41)
	kvmIoctlRun               = uintptr(0xae80)
	kvmIoctlGetVCPUMmapSize   = uintptr(0xae04)
	kvmIoctlSetUserMemRegion  = uintptr(0x4020ae46)
	kvmIoctlSetRegs           = uintptr(0x4090ae82)
	kvmIoctlSetSRegs          = uintptr(0x4138ae84)
	kvmIoctlGetSRegs          = uintptr(0x8138ae83)
	kvmExitReasonOffset       = 8  // offsetof(struct kvm_run, exit_reason)
	kvmExitIODirectionOffset  = 32 // offsetof(struct kvm_run, io.direction)
	kvmExitIOSizeOffset       = 33 // offsetof(struct kvm_run, io.size)
	kvmExitIOPortOffset       = 34 // offsetof(struct kvm_run, io.port)
	kvmExitIODataOffsetOffset = 40 // offsetof(struct kvm_run, io.data_offset)
	// The mmio member of struct kvm_run's exit-reason union starts at the
	// same offset 32 as the io member above - they are alternatives in the
	// same union, never both valid at once, selected by exit_reason.
	kvmExitMMIOPhysAddrOffset = 32 // offsetof(struct kvm_run, mmio.phys_addr)
	kvmExitMMIODataOffset     = 40 // offsetof(struct kvm_run, mmio.data)
	kvmExitMMIOLenOffset      = 48 // offsetof(struct kvm_run, mmio.len)
	kvmExitMMIOIsWriteOffset  = 52 // offsetof(struct kvm_run, mmio.is_write)
	kvmExitHLT                = 5
	kvmExitIO                 = 2
	kvmExitMMIO               = 6
	kvmExitIODirectionOut     = 1
	kvmRunImmediateExitMask   = uint32(0xff << 8) // immediate_exit is byte 1 of struct kvm_run
	// KVM_IRQ_LINE: _IOW(KVMIO, 0x61, struct kvm_irq_level) with
	// KVMIO=0xae and an 8-byte argument, i.e.
	// (1<<30)|(8<<16)|(0xae<<8)|0x61 under the standard ioctl encoding -
	// the same value every KVM VMM (crosvm, firecracker, cloud-hypervisor)
	// uses for this ioctl.
	kvmIoctlIRQLine = uintptr(0x4008ae61)
)

// kvmIRQLevel mirrors struct kvm_irq_level exactly (8 bytes: irq@0 u32,
// level@4 s32), the argument to KVM_IRQ_LINE.
type kvmIRQLevel struct {
	IRQ   uint32
	Level int32
}

// kvmUserspaceMemoryRegion mirrors struct kvm_userspace_memory_region
// exactly (32 bytes, confirmed field-by-field: slot@0 u32, flags@4 u32,
// guest_phys_addr@8 u64, memory_size@16 u64, userspace_addr@24 u64).
type kvmUserspaceMemoryRegion struct {
	Slot          uint32
	Flags         uint32
	GuestPhysAddr uint64
	MemorySize    uint64
	UserspaceAddr uint64
}

// kvmRegs mirrors struct kvm_regs exactly (144 bytes: eight 8-byte GPRs,
// r8-r15, then rip, rflags - confirmed offsets for rsp@48, rip@128,
// rflags@136).
type kvmRegs struct {
	RAX, RBX, RCX, RDX uint64
	RSI, RDI, RSP, RBP uint64
	R8, R9, R10, R11   uint64
	R12, R13, R14, R15 uint64
	RIP, RFlags        uint64
}

// kvmSegment mirrors struct kvm_segment exactly (24 bytes).
type kvmSegment struct {
	Base                                     uint64
	Limit                                    uint32
	Selector                                 uint16
	Type                                     uint8
	Present, DPL, DB, S, L, G, AVL, Unusable uint8
	Padding                                  uint8
}

// kvmDTable mirrors struct kvm_dtable exactly (16 bytes).
type kvmDTable struct {
	Base    uint64
	Limit   uint16
	Padding [3]uint16
}

// kvmSRegs mirrors struct kvm_sregs exactly (312 bytes: confirmed offsets
// for cs@0, ds@24, ss@120, cr0@224).
type kvmSRegs struct {
	CS, DS, ES, FS, GS, SS  kvmSegment
	TR, LDT                 kvmSegment
	GDT, IDT                kvmDTable
	CR0, CR2, CR3, CR4, CR8 uint64
	EFER                    uint64
	ApicBase                uint64
	InterruptBitmap         [4]uint64
}

// KVMRunResult reports what a RunFlatPayload guest did before halting or
// exhausting its step budget.
type KVMRunResult struct {
	// ExitReason is the raw KVM_EXIT_* value the guest stopped on.
	ExitReason uint32
	// HaltedCleanly is true if the guest reached KVM_EXIT_HLT.
	HaltedCleanly bool
	// PortWrites captures every `out` the guest performed to an 8-bit
	// I/O port before halting, in order - the only guest-observable
	// side effect this minimal harness understands.
	PortWrites []PortWrite
}

type PortWrite struct {
	Port uint16
	Byte byte
}

// RunFlatPayload creates a real KVM virtual machine (KVM_CREATE_VM), a
// single vCPU (KVM_CREATE_VCPU), and a guest memory region
// (KVM_SET_USER_MEMORY_REGION) of memoryBytes backed by an anonymous
// mmap, loads payload at guest physical address loadAddr, configures the
// vCPU for 16-bit real mode with RIP at loadAddr (flat CS base 0,
// selector 0, matching the well-established minimal-KVM-guest pattern),
// and runs it (KVM_RUN) until KVM_EXIT_HLT or maxSteps I/O exits are
// observed, whichever comes first.
//
// RunFlatPayload is amd64-only and does not implement the Linux boot protocol.
func RunFlatPayload(ctx context.Context, memoryBytes uint64, payload []byte, loadAddr uint64, maxSteps int) (KVMRunResult, error) {
	if err := ctx.Err(); err != nil {
		return KVMRunResult{}, err
	}
	maxInt := uint64(^uint(0) >> 1)
	if memoryBytes == 0 || memoryBytes > maxInt {
		return KVMRunResult{}, fmt.Errorf("vmm: kvm: invalid guest memory size %d", memoryBytes)
	}
	if memoryBytes%uint64(os.Getpagesize()) != 0 {
		return KVMRunResult{}, fmt.Errorf("vmm: kvm: guest memory size %d is not page-aligned", memoryBytes)
	}
	if loadAddr > 0xffff {
		return KVMRunResult{}, fmt.Errorf("vmm: kvm: load address %d exceeds the 16-bit real-mode limit", loadAddr)
	}
	if loadAddr > memoryBytes || uint64(len(payload)) > memoryBytes-loadAddr {
		return KVMRunResult{}, fmt.Errorf("vmm: kvm: payload of %d bytes does not fit at offset %d in %d bytes of memory", len(payload), loadAddr, memoryBytes)
	}
	if maxSteps <= 0 {
		return KVMRunResult{}, fmt.Errorf("vmm: kvm: max steps must be greater than zero")
	}

	kvmFile, err := os.OpenFile("/dev/kvm", os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return KVMRunResult{}, fmt.Errorf("vmm: kvm: open /dev/kvm: %w", err)
	}
	defer kvmFile.Close()
	if err := negotiateRequiredKVMExtensions(kvmExtensionChecker(kvmFile), map[string]bool{}, map[string]string{}); err != nil {
		return KVMRunResult{}, fmt.Errorf("vmm: kvm: required extensions: %w", err)
	}

	vmFD, err := ioctlNoArg(kvmFile.Fd(), kvmIoctlCreateVM)
	if err != nil {
		return KVMRunResult{}, fmt.Errorf("vmm: kvm: KVM_CREATE_VM: %w", err)
	}
	vmFile := os.NewFile(vmFD, "kvm-vm")
	defer vmFile.Close()

	guestMemory, err := syscall.Mmap(-1, 0, int(memoryBytes), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS)
	if err != nil {
		return KVMRunResult{}, fmt.Errorf("vmm: kvm: mmap guest memory: %w", err)
	}
	defer syscall.Munmap(guestMemory)
	copy(guestMemory[loadAddr:], payload)

	region := kvmUserspaceMemoryRegion{
		Slot: 0, GuestPhysAddr: 0, MemorySize: memoryBytes,
		UserspaceAddr: uint64(uintptr(unsafe.Pointer(&guestMemory[0]))),
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, vmFile.Fd(), kvmIoctlSetUserMemRegion, uintptr(unsafe.Pointer(&region))); errno != 0 {
		return KVMRunResult{}, fmt.Errorf("vmm: kvm: KVM_SET_USER_MEMORY_REGION: %w", errno)
	}

	vcpuFD, err := ioctlNoArg(vmFile.Fd(), kvmIoctlCreateVCPU)
	if err != nil {
		return KVMRunResult{}, fmt.Errorf("vmm: kvm: KVM_CREATE_VCPU: %w", err)
	}
	vcpuFile := os.NewFile(vcpuFD, "kvm-vcpu")
	defer vcpuFile.Close()

	mmapSize, err := ioctlNoArg(kvmFile.Fd(), kvmIoctlGetVCPUMmapSize)
	if err != nil {
		return KVMRunResult{}, fmt.Errorf("vmm: kvm: KVM_GET_VCPU_MMAP_SIZE: %w", err)
	}
	runRegion, err := syscall.Mmap(int(vcpuFile.Fd()), 0, int(mmapSize), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return KVMRunResult{}, fmt.Errorf("vmm: kvm: mmap kvm_run: %w", err)
	}
	defer syscall.Munmap(runRegion)
	runControl := (*uint32)(unsafe.Pointer(&runRegion[0]))
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	runThreadID := syscall.Gettid()
	stopInterrupt := make(chan struct{})
	interruptDone := make(chan struct{})
	go func() {
		defer close(interruptDone)
		select {
		case <-ctx.Done():
			setKVMImmediateExit(runControl, true)
			// immediate_exit is observed when KVM_RUN handles a host
			// signal. SIGURG is already owned by the Go runtime for
			// asynchronous preemption, so targeting this locked thread
			// interrupts the ioctl without installing a process-wide
			// application signal handler.
			_, _, _ = syscall.RawSyscall6(
				syscall.SYS_TGKILL,
				uintptr(os.Getpid()),
				uintptr(runThreadID),
				uintptr(syscall.SIGURG),
				0, 0, 0,
			)
		case <-stopInterrupt:
		}
	}()
	defer func() {
		close(stopInterrupt)
		<-interruptDone
	}()

	var sregs kvmSRegs
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, vcpuFile.Fd(), kvmIoctlGetSRegs, uintptr(unsafe.Pointer(&sregs))); errno != 0 {
		return KVMRunResult{}, fmt.Errorf("vmm: kvm: KVM_GET_SREGS: %w", errno)
	}
	// Flat real mode: base 0, selector 0, so segment:offset addressing
	// collapses to a plain physical address and RIP can be set directly
	// to loadAddr - the standard minimal-KVM-guest setup.
	sregs.CS = kvmSegment{Base: 0, Selector: 0, Limit: 0xffff, Type: 11, Present: 1, S: 1}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, vcpuFile.Fd(), kvmIoctlSetSRegs, uintptr(unsafe.Pointer(&sregs))); errno != 0 {
		return KVMRunResult{}, fmt.Errorf("vmm: kvm: KVM_SET_SREGS: %w", errno)
	}

	regs := kvmRegs{RIP: loadAddr, RFlags: 0x2} // bit 1 is reserved-must-be-1 in EFLAGS
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, vcpuFile.Fd(), kvmIoctlSetRegs, uintptr(unsafe.Pointer(&regs))); errno != 0 {
		return KVMRunResult{}, fmt.Errorf("vmm: kvm: KVM_SET_REGS: %w", errno)
	}

	var result KVMRunResult
	for step := 0; step < maxSteps; step++ {
		setKVMImmediateExit(runControl, false)
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, vcpuFile.Fd(), kvmIoctlRun, 0); errno != 0 {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			return result, fmt.Errorf("vmm: kvm: KVM_RUN: %w", errno)
		}
		exitReason := *(*uint32)(unsafe.Pointer(&runRegion[kvmExitReasonOffset]))
		result.ExitReason = exitReason
		switch exitReason {
		case kvmExitHLT:
			result.HaltedCleanly = true
			return result, nil
		case kvmExitIO:
			direction := runRegion[kvmExitIODirectionOffset]
			size := runRegion[kvmExitIOSizeOffset]
			port := *(*uint16)(unsafe.Pointer(&runRegion[kvmExitIOPortOffset]))
			dataOffset := *(*uint64)(unsafe.Pointer(&runRegion[kvmExitIODataOffsetOffset]))
			if direction == kvmExitIODirectionOut && size == 1 {
				if dataOffset >= uint64(len(runRegion)) {
					return result, fmt.Errorf("vmm: kvm: I/O data offset %d exceeds kvm_run mapping", dataOffset)
				}
				result.PortWrites = append(result.PortWrites, PortWrite{Port: port, Byte: runRegion[dataOffset]})
			}
		default:
			return result, fmt.Errorf("vmm: kvm: unhandled exit reason %d", exitReason)
		}
	}
	return result, fmt.Errorf("vmm: kvm: guest did not halt within %d steps", maxSteps)
}

// setKVMImmediateExit atomically updates only byte 1 of struct kvm_run. The
// surrounding bytes are distinct ABI fields and must be preserved.
func setKVMImmediateExit(control *uint32, enabled bool) {
	for {
		current := atomic.LoadUint32(control)
		next := current &^ kvmRunImmediateExitMask
		if enabled {
			next |= 1 << 8
		}
		if atomic.CompareAndSwapUint32(control, current, next) {
			return
		}
	}
}

// ioctlNoArg issues an argument-less ioctl (KVM_CREATE_VM,
// KVM_CREATE_VCPU, KVM_GET_VCPU_MMAP_SIZE all return their result as the
// ioctl's own return value, not through an output parameter) and returns
// that value as a file descriptor / size.
func ioctlNoArg(fd uintptr, request uintptr) (uintptr, error) {
	result, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, 0)
	if errno != 0 {
		return 0, errno
	}
	return result, nil
}

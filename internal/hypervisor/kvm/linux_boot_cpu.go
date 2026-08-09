package kvm

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	linuxBootGDTAddress   = uint64(0x0000_0500)
	linuxBootCodeSelector = uint16(0x08)
	linuxBootDataSelector = uint16(0x10)
	linuxCR0ProtectedMode = uint64(1)
)

// linuxBootCPUState is the architecture-neutral description consumed by the
// linux/amd64 KVM ioctl adapter. Keeping this calculation free of KVM and host
// build tags makes the boot contract testable on every development platform.
type linuxBootCPUState struct {
	EntryPoint   uint64
	BootParams   uint64
	StackPointer uint64
	RFlags       uint64
	CR0Set       uint64
	GDTAddress   uint64
	GDTLimit     uint16
	CodeSelector uint16
	DataSelector uint16
}

// prepareLinuxBootCPU installs the flat 32-bit GDT required by the x86 Linux
// boot protocol and returns the initial register contract. The compressed
// kernel's 32-bit entry point receives the boot_params address in ESI.
func prepareLinuxBootCPU(memory []byte, layout LinuxBootLayout) (linuxBootCPUState, error) {
	if layout.EntryPoint == 0 || layout.EntryPoint >= uint64(len(memory)) {
		return linuxBootCPUState{}, errors.New("vmm: linux boot: entry point is outside guest memory")
	}
	if !fits(memory, layout.BootParamsAddress, linuxBootParamsSize) {
		return linuxBootCPUState{}, errors.New("vmm: linux boot: boot_params is outside guest memory")
	}
	const gdtBytes = 3 * 8
	if !fits(memory, linuxBootGDTAddress, gdtBytes) {
		return linuxBootCPUState{}, errors.New("vmm: linux boot: GDT is outside guest memory")
	}
	if rangesOverlap(linuxBootGDTAddress, gdtBytes, layout.BootParamsAddress, linuxBootParamsSize) ||
		rangesOverlap(linuxBootGDTAddress, gdtBytes, layout.KernelAddress, layout.KernelSize) ||
		rangesOverlap(linuxBootGDTAddress, gdtBytes, layout.InitrdAddress, layout.InitrdSize) {
		return linuxBootCPUState{}, errors.New("vmm: linux boot: GDT overlaps boot content")
	}

	gdt := memory[linuxBootGDTAddress : linuxBootGDTAddress+gdtBytes]
	clear(gdt)
	// Flat 0..4GiB code and data descriptors: base=0, limit=0xfffff with
	// granularity=4KiB, present, ring 0, 32-bit default operand size.
	binary.LittleEndian.PutUint64(gdt[8:16], 0x00cf_9a00_0000_ffff)
	binary.LittleEndian.PutUint64(gdt[16:24], 0x00cf_9200_0000_ffff)

	return linuxBootCPUState{
		EntryPoint:   layout.EntryPoint,
		BootParams:   layout.BootParamsAddress,
		StackPointer: 0x0009_0000,
		RFlags:       0x2, CR0Set: linuxCR0ProtectedMode,
		GDTAddress: linuxBootGDTAddress, GDTLimit: gdtBytes - 1,
		CodeSelector: linuxBootCodeSelector, DataSelector: linuxBootDataSelector,
	}, nil
}

func rangesOverlap(a, aSize, b, bSize uint64) bool {
	if aSize == 0 || bSize == 0 {
		return false
	}
	aEnd, bEnd := a+aSize, b+bSize
	if aEnd < a || bEnd < b {
		return true
	}
	return a < bEnd && b < aEnd
}

func validateLinuxBootCPUState(state linuxBootCPUState) error {
	if state.RFlags&0x2 == 0 || state.CR0Set&linuxCR0ProtectedMode == 0 {
		return fmt.Errorf("vmm: linux boot: invalid protected-mode CPU state")
	}
	if state.CodeSelector == 0 || state.DataSelector == 0 || state.CodeSelector == state.DataSelector {
		return fmt.Errorf("vmm: linux boot: invalid segment selectors")
	}
	return nil
}

//go:build linux && amd64

package kvm

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// configureLinuxBootVCPU bridges LoadLinuxBoot's memory layout to KVM. It
// enters 32-bit protected mode with flat segments and the boot_params pointer
// in RSI, as required by the x86 Linux boot protocol's 32-bit entry point.
func configureLinuxBootVCPU(vcpu *os.File, memory []byte, layout LinuxBootLayout) error {
	if vcpu == nil {
		return fmt.Errorf("vmm: kvm: linux boot: nil vCPU")
	}
	state, err := prepareLinuxBootCPU(memory, layout)
	if err != nil {
		return err
	}
	if err := validateLinuxBootCPUState(state); err != nil {
		return err
	}

	var sregs kvmSRegs
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, vcpu.Fd(), kvmIoctlGetSRegs, uintptr(unsafe.Pointer(&sregs))); errno != 0 {
		return fmt.Errorf("vmm: kvm: linux boot: KVM_GET_SREGS: %w", errno)
	}
	code := kvmSegment{
		Base: 0, Limit: 0xffff_ffff, Selector: state.CodeSelector,
		Type: 11, Present: 1, S: 1, DB: 1, G: 1,
	}
	data := kvmSegment{
		Base: 0, Limit: 0xffff_ffff, Selector: state.DataSelector,
		Type: 3, Present: 1, S: 1, DB: 1, G: 1,
	}
	sregs.CS = code
	sregs.DS, sregs.ES, sregs.FS, sregs.GS, sregs.SS = data, data, data, data, data
	sregs.GDT = kvmDTable{Base: state.GDTAddress, Limit: state.GDTLimit}
	sregs.CR0 |= state.CR0Set
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, vcpu.Fd(), kvmIoctlSetSRegs, uintptr(unsafe.Pointer(&sregs))); errno != 0 {
		return fmt.Errorf("vmm: kvm: linux boot: KVM_SET_SREGS: %w", errno)
	}

	regs := kvmRegs{
		RSI: state.BootParams, RSP: state.StackPointer,
		RIP: state.EntryPoint, RFlags: state.RFlags,
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, vcpu.Fd(), kvmIoctlSetRegs, uintptr(unsafe.Pointer(&regs))); errno != 0 {
		return fmt.Errorf("vmm: kvm: linux boot: KVM_SET_REGS: %w", errno)
	}
	return nil
}

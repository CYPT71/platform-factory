package kvm

import (
	"encoding/binary"
	"testing"
)

func TestPrepareLinuxBootCPUCreatesProtectedModeContract(t *testing.T) {
	memory := make([]byte, 8<<20)
	layout := LinuxBootLayout{
		BootParamsAddress: linuxBootParamsAddress,
		KernelAddress:     linuxKernelLoadAddress, KernelSize: 4096,
		EntryPoint: linuxKernelLoadAddress,
	}
	state, err := prepareLinuxBootCPU(memory, layout)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateLinuxBootCPUState(state); err != nil {
		t.Fatal(err)
	}
	if state.EntryPoint != layout.EntryPoint || state.BootParams != layout.BootParamsAddress {
		t.Fatalf("register contract=%+v", state)
	}
	if state.CodeSelector != 0x08 || state.DataSelector != 0x10 ||
		state.GDTLimit != 23 || state.CR0Set&1 == 0 {
		t.Fatalf("protected-mode state=%+v", state)
	}
	gdt := memory[state.GDTAddress : state.GDTAddress+24]
	if binary.LittleEndian.Uint64(gdt[8:16]) != 0x00cf_9a00_0000_ffff ||
		binary.LittleEndian.Uint64(gdt[16:24]) != 0x00cf_9200_0000_ffff {
		t.Fatalf("GDT=%x", gdt)
	}
}

func TestPrepareLinuxBootCPURejectsInvalidOrOverlappingLayout(t *testing.T) {
	memory := make([]byte, 2<<20)
	for name, layout := range map[string]LinuxBootLayout{
		"entry": {
			BootParamsAddress: linuxBootParamsAddress,
			EntryPoint:        uint64(len(memory)),
		},
		"params": {
			BootParamsAddress: uint64(len(memory)),
			EntryPoint:        linuxKernelLoadAddress,
		},
		"gdt overlap": {
			BootParamsAddress: linuxBootParamsAddress,
			KernelAddress:     linuxBootGDTAddress, KernelSize: 1,
			EntryPoint: linuxKernelLoadAddress,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := prepareLinuxBootCPU(memory, layout); err == nil {
				t.Fatal("invalid CPU layout accepted")
			}
		})
	}
}

func TestValidateLinuxBootCPUStateRejectsInvalidContracts(t *testing.T) {
	valid := linuxBootCPUState{RFlags: 0x2, CR0Set: linuxCR0ProtectedMode, CodeSelector: 0x08, DataSelector: 0x10}
	for name, state := range map[string]linuxBootCPUState{
		"reserved RFlags bit clear": {RFlags: 0, CR0Set: linuxCR0ProtectedMode, CodeSelector: 0x08, DataSelector: 0x10},
		"protected mode not set":    {RFlags: 0x2, CR0Set: 0, CodeSelector: 0x08, DataSelector: 0x10},
		"zero code selector":        {RFlags: 0x2, CR0Set: linuxCR0ProtectedMode, CodeSelector: 0, DataSelector: 0x10},
		"zero data selector":        {RFlags: 0x2, CR0Set: linuxCR0ProtectedMode, CodeSelector: 0x08, DataSelector: 0},
		"aliased selectors":         {RFlags: 0x2, CR0Set: linuxCR0ProtectedMode, CodeSelector: 0x08, DataSelector: 0x08},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateLinuxBootCPUState(state); err == nil {
				t.Fatal("invalid CPU state accepted")
			}
		})
	}
	if err := validateLinuxBootCPUState(valid); err != nil {
		t.Fatalf("valid CPU state rejected: %v", err)
	}
}

func TestRangesOverlapHandlesEmptyAndOverflowingRanges(t *testing.T) {
	if rangesOverlap(1, 0, 1, 1) {
		t.Fatal("empty range overlaps")
	}
	if !rangesOverlap(^uint64(0)-1, 4, 0, 1) {
		t.Fatal("overflowing range accepted")
	}
}

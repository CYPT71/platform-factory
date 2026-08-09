package kvm

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestLoadLinuxBootPlacesAllInputsAndBootParams(t *testing.T) {
	kernel := testBZImage(4, bytes.Repeat([]byte{0xa5}, 8192))
	initrd := bytes.Repeat([]byte{0x5a}, 12345)
	memory := make([]byte, 32<<20)
	layout, err := LoadLinuxBoot(memory, kernel, initrd, "console=ttyS0 panic=-1")
	if err != nil {
		t.Fatal(err)
	}
	if layout.KernelAddress != linuxKernelLoadAddress ||
		!bytes.Equal(memory[layout.KernelAddress:layout.KernelAddress+layout.KernelSize], kernel[5*512:]) {
		t.Fatal("protected-mode kernel payload was not placed at 1 MiB")
	}
	if layout.InitrdAddress%linuxPageSize != 0 ||
		!bytes.Equal(memory[layout.InitrdAddress:layout.InitrdAddress+layout.InitrdSize], initrd) {
		t.Fatal("initrd was not placed intact at a page-aligned high address")
	}
	params := memory[linuxBootParamsAddress : linuxBootParamsAddress+linuxBootParamsSize]
	if got := binary.LittleEndian.Uint32(params[linuxCommandLineOffset:]); got != uint32(layout.CommandLineAddress) {
		t.Fatalf("cmd_line_ptr=%#x", got)
	}
	if got := binary.LittleEndian.Uint32(params[linuxRamdiskImageOffset:]); got != uint32(layout.InitrdAddress) {
		t.Fatalf("ramdisk_image=%#x", got)
	}
	if params[linuxE820EntriesOffset] != 2 {
		t.Fatalf("e820_entries=%d", params[linuxE820EntriesOffset])
	}
	command := memory[layout.CommandLineAddress : layout.CommandLineAddress+layout.CommandLineSize]
	if string(command) != "console=ttyS0 panic=-1\x00" {
		t.Fatalf("command line=%q", command)
	}
}

func TestLoadLinuxBootRejectsMalformedOrUnsafeInputs(t *testing.T) {
	valid := testBZImage(4, []byte{0xf4})
	for name, kernel := range map[string][]byte{
		"short":   make([]byte, 16),
		"magic":   append([]byte(nil), valid...),
		"zimage":  append([]byte(nil), valid...),
		"no-body": append([]byte(nil), valid[:5*512]...),
	} {
		switch name {
		case "magic":
			kernel[linuxHeaderOffset] = 0
		case "zimage":
			kernel[linuxLoadFlagsOffset] = 0
		}
		t.Run(name, func(t *testing.T) {
			if _, err := LoadLinuxBoot(make([]byte, 8<<20), kernel, nil, ""); err == nil {
				t.Fatal("invalid kernel accepted")
			}
		})
	}
	if _, err := LoadLinuxBoot(make([]byte, 8<<20), valid, nil, "bad\x00argument"); err == nil {
		t.Fatal("NUL command line accepted")
	}
	if _, err := LoadLinuxBoot(make([]byte, 8<<20), valid, nil, strings.Repeat("x", 4096)); err == nil {
		t.Fatal("oversized command line accepted")
	}
}

func TestLoadLinuxBootRejectsInitrdOverlap(t *testing.T) {
	kernel := testBZImage(4, bytes.Repeat([]byte{1}, 3<<20))
	binary.LittleEndian.PutUint32(kernel[linuxInitSizeOffset:], 6<<20)
	if _, err := LoadLinuxBoot(make([]byte, 8<<20), kernel, bytes.Repeat([]byte{2}, 3<<20), ""); err == nil ||
		!strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("expected overlap rejection, got %v", err)
	}
}

func TestLoadLinuxBootWithEntropyWritesSetupRNGSeed(t *testing.T) {
	kernel := testBZImage(4, bytes.Repeat([]byte{0xa5}, 8192))
	memory := make([]byte, 32<<20)
	seed := bytes.Repeat([]byte{0x6d}, 64)
	layout, err := LoadLinuxBootWithEntropy(memory, kernel, []byte("initrd"), "console=ttyS0", seed)
	if err != nil {
		t.Fatal(err)
	}
	if layout.SetupDataAddress != linuxSetupDataAddress ||
		layout.SetupDataSize != linuxSetupDataHeaderSize+uint64(len(seed)) {
		t.Fatalf("setup_data layout=%+v", layout)
	}
	params := memory[layout.BootParamsAddress : layout.BootParamsAddress+linuxBootParamsSize]
	if got := binary.LittleEndian.Uint64(params[linuxSetupDataOffset:]); got != layout.SetupDataAddress {
		t.Fatalf("setup_data pointer=%#x want %#x", got, layout.SetupDataAddress)
	}
	record := memory[layout.SetupDataAddress : layout.SetupDataAddress+layout.SetupDataSize]
	if next := binary.LittleEndian.Uint64(record); next != 0 {
		t.Fatalf("setup_data next=%#x", next)
	}
	if kind := binary.LittleEndian.Uint32(record[8:]); kind != linuxSetupRNGSeed {
		t.Fatalf("setup_data type=%d", kind)
	}
	if length := binary.LittleEndian.Uint32(record[12:]); length != uint32(len(seed)) {
		t.Fatalf("setup_data len=%d", length)
	}
	if !bytes.Equal(record[linuxSetupDataHeaderSize:], seed) {
		t.Fatal("entropy seed was not copied intact")
	}
	for name, region := range map[string][2]uint64{
		"boot params": {layout.BootParamsAddress, linuxBootParamsSize},
		"kernel":      {layout.KernelAddress, layout.KernelSize},
		"initrd":      {layout.InitrdAddress, layout.InitrdSize},
		"command":     {layout.CommandLineAddress, layout.CommandLineSize},
	} {
		if rangesOverlap(layout.SetupDataAddress, layout.SetupDataSize, region[0], region[1]) {
			t.Fatalf("setup_data overlaps %s", name)
		}
	}
}

func TestLoadLinuxBootEntropyCompatibilityAndValidation(t *testing.T) {
	kernel := testBZImage(4, []byte{0xf4})
	memory := make([]byte, 8<<20)
	if _, err := LoadLinuxBootWithEntropy(memory, kernel, nil, "", nil); err == nil {
		t.Fatal("empty entropy seed accepted")
	}
	old := append([]byte(nil), kernel...)
	binary.LittleEndian.PutUint16(old[linuxVersionOffset:], 0x0208)
	if _, err := LoadLinuxBootWithEntropy(memory, old, nil, "", []byte("seed")); err == nil ||
		!strings.Contains(err.Error(), "2.09") {
		t.Fatalf("old protocol entropy err=%v", err)
	}
	layout, err := LoadLinuxBoot(memory, old, nil, "")
	if err != nil {
		t.Fatalf("legacy boot without entropy was rejected: %v", err)
	}
	if layout.SetupDataAddress != 0 || layout.SetupDataSize != 0 {
		t.Fatalf("legacy setup_data=%+v", layout)
	}
}

func testBZImage(setupSectors byte, payload []byte) []byte {
	kernel := make([]byte, (int(setupSectors)+1)*512+len(payload))
	kernel[linuxSetupSectorsOffset] = setupSectors
	binary.LittleEndian.PutUint16(kernel[linuxBootFlagOffset:], 0xaa55)
	copy(kernel[linuxHeaderOffset:], "HdrS")
	binary.LittleEndian.PutUint16(kernel[linuxVersionOffset:], 0x020a)
	kernel[linuxLoadFlagsOffset] = linuxLoadFlagLoadedHigh
	binary.LittleEndian.PutUint32(kernel[linuxInitrdMaxOffset:], 0x37ff_ffff)
	binary.LittleEndian.PutUint32(kernel[linuxCommandLineMax:], 2048)
	binary.LittleEndian.PutUint32(kernel[linuxInitSizeOffset:], uint32(len(payload)))
	copy(kernel[(int(setupSectors)+1)*512:], payload)
	return kernel
}

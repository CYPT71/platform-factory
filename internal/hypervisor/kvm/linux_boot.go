package kvm

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	linuxBootParamsAddress  = uint64(0x0000_7000)
	linuxSetupDataAddress   = uint64(0x0001_0000)
	linuxCommandLineAddress = uint64(0x0002_0000)
	linuxKernelLoadAddress  = uint64(0x0010_0000)
	linuxBootParamsSize     = uint64(4096)
	linuxPageSize           = uint64(4096)

	linuxSetupSectorsOffset = 0x1f1
	linuxBootFlagOffset     = 0x1fe
	linuxHeaderOffset       = 0x202
	linuxVersionOffset      = 0x206
	linuxTypeOfLoaderOffset = 0x210
	linuxCode32StartOffset  = 0x214
	linuxLoadFlagsOffset    = 0x211
	linuxRamdiskImageOffset = 0x218
	linuxRamdiskSizeOffset  = 0x21c
	linuxHeapEndOffset      = 0x224
	linuxCommandLineOffset  = 0x228
	linuxInitrdMaxOffset    = 0x22c
	linuxCommandLineMax     = 0x238
	linuxSetupDataOffset    = 0x250
	linuxInitSizeOffset     = 0x260
	// End of the setup_header fields understood by protocol 2.15. Bytes
	// outside setup_header are setup code, not boot_params, and must not be
	// copied into the zero page.
	linuxSetupHeaderEnd = 0x268

	linuxAltMemKOffset     = 0x1e0
	linuxE820EntriesOffset = 0x1e8
	linuxExtMemKOffset     = 0x002
	linuxE820TableOffset   = 0x2d0
	linuxE820EntrySize     = 20

	linuxLoadFlagLoadedHigh  = 0x01
	linuxLoadFlagCanUseHeap  = 0x80
	linuxE820RAM             = 1
	linuxSetupDataHeaderSize = uint64(16)
	linuxSetupRNGSeed        = uint32(9)
)

// LinuxBootLayout describes exactly where LoadLinuxBoot placed each input in
// guest physical memory. It is architecture-neutral data, but represents the
// x86 Linux boot protocol used by the native amd64 KVM backend.
type LinuxBootLayout struct {
	BootParamsAddress  uint64
	KernelAddress      uint64
	KernelSize         uint64
	InitrdAddress      uint64
	InitrdSize         uint64
	CommandLineAddress uint64
	CommandLineSize    uint64
	SetupDataAddress   uint64
	SetupDataSize      uint64
	EntryPoint         uint64
}

// LoadLinuxBoot validates an x86 Linux bzImage and installs its protected-mode
// payload, optional initrd, NUL-terminated command line, boot_params and E820
// map into memory. No host tool or firmware is involved.
//
// This prepares guest memory only. The KVM backend must still configure the
// x86 boot CPU state, interrupt controller and devices before entering KVM_RUN.
func LoadLinuxBoot(memory, kernel, initrd []byte, commandLine string) (LinuxBootLayout, error) {
	return loadLinuxBoot(memory, kernel, initrd, commandLine, nil)
}

// LoadLinuxBootWithEntropy additionally installs entropy through the x86
// boot protocol's SETUP_RNG_SEED setup_data record. The caller owns seed and
// should erase it after this function returns.
func LoadLinuxBootWithEntropy(memory, kernel, initrd []byte, commandLine string, seed []byte) (LinuxBootLayout, error) {
	if len(seed) == 0 || uint64(len(seed)) > uint64(^uint32(0)) {
		return LinuxBootLayout{}, errors.New("vmm: linux boot: entropy seed must be non-empty and fit the setup_data length field")
	}
	return loadLinuxBoot(memory, kernel, initrd, commandLine, seed)
}

func loadLinuxBoot(memory, kernel, initrd []byte, commandLine string, seed []byte) (LinuxBootLayout, error) {
	if uint64(len(memory)) <= linuxKernelLoadAddress {
		return LinuxBootLayout{}, fmt.Errorf("vmm: linux boot: guest memory must exceed %d bytes", linuxKernelLoadAddress)
	}
	header, err := parseLinuxBootHeader(kernel)
	if err != nil {
		return LinuxBootLayout{}, err
	}
	if bytes.IndexByte([]byte(commandLine), 0) >= 0 {
		return LinuxBootLayout{}, errors.New("vmm: linux boot: command line contains NUL")
	}
	commandBytes := append([]byte(commandLine), 0)
	if uint64(len(commandBytes)) > uint64(header.commandLineMax) {
		return LinuxBootLayout{}, fmt.Errorf(
			"vmm: linux boot: command line is %d bytes including NUL; kernel limit is %d",
			len(commandBytes), header.commandLineMax)
	}

	payload := kernel[header.payloadOffset:]
	kernelFootprint := uint64(len(payload))
	if header.initSize > kernelFootprint {
		kernelFootprint = header.initSize
	}
	if !fits(memory, linuxKernelLoadAddress, kernelFootprint) {
		return LinuxBootLayout{}, errors.New("vmm: linux boot: kernel does not fit in guest memory")
	}
	if !fits(memory, linuxBootParamsAddress, linuxBootParamsSize) ||
		!fits(memory, linuxCommandLineAddress, uint64(len(commandBytes))) {
		return LinuxBootLayout{}, errors.New("vmm: linux boot: boot parameters do not fit in guest memory")
	}
	setupDataSize := uint64(0)
	if len(seed) != 0 {
		if header.version < 0x0209 {
			return LinuxBootLayout{}, fmt.Errorf(
				"vmm: linux boot: protocol %#04x cannot accept setup_data entropy (need 2.09)", header.version)
		}
		setupDataSize = linuxSetupDataHeaderSize + uint64(len(seed))
		if !fits(memory, linuxSetupDataAddress, setupDataSize) ||
			rangesOverlap(linuxSetupDataAddress, setupDataSize, linuxBootParamsAddress, linuxBootParamsSize) ||
			rangesOverlap(linuxSetupDataAddress, setupDataSize, linuxCommandLineAddress, uint64(len(commandBytes))) ||
			rangesOverlap(linuxSetupDataAddress, setupDataSize, linuxKernelLoadAddress, kernelFootprint) {
			return LinuxBootLayout{}, errors.New("vmm: linux boot: entropy setup_data does not fit without overlapping boot content")
		}
	}

	initrdAddress, err := placeLinuxInitrd(uint64(len(memory)), uint64(len(initrd)),
		header.initrdAddressMax, linuxKernelLoadAddress+kernelFootprint)
	if err != nil {
		return LinuxBootLayout{}, err
	}

	params := memory[linuxBootParamsAddress : linuxBootParamsAddress+linuxBootParamsSize]
	clear(params)
	headerEnd := min(len(kernel), linuxSetupHeaderEnd)
	copy(params[linuxSetupSectorsOffset:headerEnd], kernel[linuxSetupSectorsOffset:headerEnd])
	params[linuxTypeOfLoaderOffset] = 0xff // unknown, non-bootloader userspace loader
	params[linuxLoadFlagsOffset] |= linuxLoadFlagCanUseHeap
	binary.LittleEndian.PutUint16(params[linuxHeapEndOffset:], 0xfe00)
	binary.LittleEndian.PutUint32(params[linuxCommandLineOffset:], uint32(linuxCommandLineAddress))
	if len(seed) != 0 {
		binary.LittleEndian.PutUint64(params[linuxSetupDataOffset:], linuxSetupDataAddress)
		record := memory[linuxSetupDataAddress : linuxSetupDataAddress+setupDataSize]
		clear(record)
		binary.LittleEndian.PutUint32(record[8:], linuxSetupRNGSeed)
		binary.LittleEndian.PutUint32(record[12:], uint32(len(seed)))
		copy(record[linuxSetupDataHeaderSize:], seed)
	}
	if len(initrd) > 0 {
		binary.LittleEndian.PutUint32(params[linuxRamdiskImageOffset:], uint32(initrdAddress))
		binary.LittleEndian.PutUint32(params[linuxRamdiskSizeOffset:], uint32(len(initrd)))
	}
	writeLinuxMemoryMap(params, uint64(len(memory)))

	copy(memory[linuxKernelLoadAddress:], payload)
	copy(memory[linuxCommandLineAddress:], commandBytes)
	if len(initrd) > 0 {
		copy(memory[initrdAddress:], initrd)
	}
	setupDataAddress := uint64(0)
	if len(seed) != 0 {
		setupDataAddress = linuxSetupDataAddress
	}
	return LinuxBootLayout{
		BootParamsAddress:  linuxBootParamsAddress,
		KernelAddress:      linuxKernelLoadAddress,
		KernelSize:         uint64(len(payload)),
		InitrdAddress:      initrdAddress,
		InitrdSize:         uint64(len(initrd)),
		CommandLineAddress: linuxCommandLineAddress,
		CommandLineSize:    uint64(len(commandBytes)),
		SetupDataAddress:   setupDataAddress,
		SetupDataSize:      setupDataSize,
		EntryPoint:         header.entryPoint,
	}, nil
}

type linuxBootHeader struct {
	version          uint16
	payloadOffset    int
	entryPoint       uint64
	commandLineMax   uint32
	initrdAddressMax uint64
	initSize         uint64
}

func parseLinuxBootHeader(kernel []byte) (linuxBootHeader, error) {
	if len(kernel) < linuxInitSizeOffset+4 {
		return linuxBootHeader{}, errors.New("vmm: linux boot: kernel is too small for a bzImage header")
	}
	if binary.LittleEndian.Uint16(kernel[linuxBootFlagOffset:]) != 0xaa55 ||
		string(kernel[linuxHeaderOffset:linuxHeaderOffset+4]) != "HdrS" {
		return linuxBootHeader{}, errors.New("vmm: linux boot: kernel has no valid x86 bzImage header")
	}
	version := binary.LittleEndian.Uint16(kernel[linuxVersionOffset:])
	if version < 0x0200 {
		return linuxBootHeader{}, fmt.Errorf("vmm: linux boot: protocol %#04x is older than 2.00", version)
	}
	if kernel[linuxLoadFlagsOffset]&linuxLoadFlagLoadedHigh == 0 {
		return linuxBootHeader{}, errors.New("vmm: linux boot: low-loaded zImage kernels are unsupported")
	}
	setupSectors := int(kernel[linuxSetupSectorsOffset])
	if setupSectors == 0 {
		setupSectors = 4
	}
	payloadOffset := (setupSectors + 1) * 512
	if payloadOffset >= len(kernel) {
		return linuxBootHeader{}, errors.New("vmm: linux boot: setup sectors consume the whole kernel")
	}
	commandLineMax := uint32(255)
	if version >= 0x0206 {
		commandLineMax = binary.LittleEndian.Uint32(kernel[linuxCommandLineMax:])
		if commandLineMax == 0 {
			commandLineMax = 255
		}
	}
	initrdMax := uint64(0x37ff_ffff)
	if version >= 0x0203 {
		initrdMax = uint64(binary.LittleEndian.Uint32(kernel[linuxInitrdMaxOffset:]))
	}
	initSize := uint64(0)
	if version >= 0x020a {
		initSize = uint64(binary.LittleEndian.Uint32(kernel[linuxInitSizeOffset:]))
	}
	entryPoint := uint64(binary.LittleEndian.Uint32(
		kernel[linuxCode32StartOffset:],
	))
	if entryPoint == 0 {
		entryPoint = linuxKernelLoadAddress
	}

	return linuxBootHeader{
		version:          version,
		payloadOffset:    payloadOffset,
		entryPoint:       entryPoint,
		commandLineMax:   commandLineMax,
		initrdAddressMax: initrdMax,
		initSize:         initSize,
	}, nil
}

func placeLinuxInitrd(memorySize, initrdSize, initrdMax, kernelEnd uint64) (uint64, error) {
	if initrdSize == 0 {
		return 0, nil
	}
	if initrdSize > uint64(^uint32(0)) {
		return 0, errors.New("vmm: linux boot: initrd exceeds the x86 boot protocol size field")
	}
	top := memorySize
	if initrdMax != ^uint64(0) && top > initrdMax+1 {
		top = initrdMax + 1
	}
	if initrdSize > top {
		return 0, errors.New("vmm: linux boot: initrd does not fit below the kernel limit")
	}
	address := (top - initrdSize) &^ (linuxPageSize - 1)
	if address < kernelEnd {
		return 0, errors.New("vmm: linux boot: initrd overlaps the loaded kernel")
	}
	return address, nil
}

func fits(memory []byte, address, size uint64) bool {
	return address <= uint64(len(memory)) && size <= uint64(len(memory))-address
}

func writeLinuxMemoryMap(params []byte, memorySize uint64) {
	type region struct{ address, size uint64 }
	regions := []region{{0, 0x0009_fc00}}
	if memorySize > linuxKernelLoadAddress {
		regions = append(regions, region{linuxKernelLoadAddress, memorySize - linuxKernelLoadAddress})
	}
	params[linuxE820EntriesOffset] = byte(len(regions))
	for index, item := range regions {
		offset := linuxE820TableOffset + index*linuxE820EntrySize
		binary.LittleEndian.PutUint64(params[offset:], item.address)
		binary.LittleEndian.PutUint64(params[offset+8:], item.size)
		binary.LittleEndian.PutUint32(params[offset+16:], linuxE820RAM)
	}
	if memorySize > linuxKernelLoadAddress {
		extendedKiB := (memorySize - linuxKernelLoadAddress) >> 10
		binary.LittleEndian.PutUint32(params[linuxAltMemKOffset:], uint32(min(extendedKiB, uint64(^uint32(0)))))
		binary.LittleEndian.PutUint16(params[linuxExtMemKOffset:], uint16(min(extendedKiB, uint64(^uint16(0)))))
	}
}

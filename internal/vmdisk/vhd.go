package vmdisk

import (
	"bytes"
	"fmt"
	"os"
)

// vhdLogicalDisk maps a VHD's logical address space to file offsets.
// Fixed VHDs need no mapping (logical offset == file offset); dynamic
// VHDs are mapped one Block Allocation Table entry at a time.
// Differencing VHDs (which read from a separate parent file) are
// explicitly not supported - see the ErrCannotMapLogicalDisk return in
// newVHDLogicalDisk.
type vhdLogicalDisk struct {
	file *os.File
	// fixed is true when logical offset == file offset directly.
	fixed bool

	// Dynamic-disk fields, unused when fixed is true.
	batOffset   int64
	blockSize   int64
	bitmapBytes int64 // sector-bitmap size preceding each block's data, rounded up to a sector
	maxEntries  uint32
}

const (
	vhdFooterSize  = 512
	vhdDiskFixed   = 2
	vhdDiskDynamic = 3
)

func newVHDLogicalDisk(file *os.File) (*vhdLogicalDisk, error) {
	stat, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("vmdisk: stat vhd: %w", err)
	}
	if stat.Size() < vhdFooterSize {
		return nil, fmt.Errorf("vmdisk: vhd file too small for a footer")
	}
	footer, err := readAtBounded(file, stat.Size()-vhdFooterSize, vhdFooterSize)
	if err != nil {
		return nil, fmt.Errorf("vmdisk: read vhd footer: %w", err)
	}
	if !bytes.Equal(footer[:8], []byte("conectix")) {
		// The leading copy (if any) is only ever a hint for detection;
		// the trailing footer is authoritative for mapping.
		return nil, fmt.Errorf("%w: vhd footer missing at end of file", ErrCannotMapLogicalDisk)
	}
	diskType := be32(footer[60:64])
	switch diskType {
	case vhdDiskFixed:
		return &vhdLogicalDisk{file: file, fixed: true}, nil
	case vhdDiskDynamic:
		dataOffset := int64(be64(footer[16:24]))
		return newDynamicVHD(file, dataOffset)
	default:
		return nil, fmt.Errorf("%w: vhd disk type %d (differencing or unknown) is not supported", ErrCannotMapLogicalDisk, diskType)
	}
}

func newDynamicVHD(file *os.File, dataOffset int64) (*vhdLogicalDisk, error) {
	header, err := readAtBounded(file, dataOffset, 1024)
	if err != nil {
		return nil, fmt.Errorf("vmdisk: read vhd dynamic header: %w", err)
	}
	if !bytes.Equal(header[:8], []byte("cxsparse")) {
		return nil, fmt.Errorf("%w: vhd dynamic disk header cookie missing", ErrCannotMapLogicalDisk)
	}
	tableOffset := int64(be64(header[16:24]))
	maxEntries := be32(header[28:32])
	blockSize := int64(be32(header[32:36]))
	if blockSize <= 0 || blockSize > maxMappedRegionSize || maxEntries > (1<<24) {
		return nil, fmt.Errorf("%w: implausible vhd block size=%d or entry count=%d", ErrCannotMapLogicalDisk, blockSize, maxEntries)
	}
	bitmapBits := blockSize / 512
	bitmapBytes := (bitmapBits + 7) / 8
	bitmapSectors := (bitmapBytes + 511) / 512
	return &vhdLogicalDisk{
		file: file, fixed: false,
		batOffset: tableOffset, blockSize: blockSize,
		bitmapBytes: bitmapSectors * 512, maxEntries: maxEntries,
	}, nil
}

func (v *vhdLogicalDisk) ReadLogical(offset, length int64) ([]byte, error) {
	if v.fixed {
		return readAtBounded(v.file, offset, length)
	}
	out := make([]byte, 0, length)
	for remaining := length; remaining > 0; {
		blockIndex := offset / v.blockSize
		if blockIndex < 0 || uint32(blockIndex) >= v.maxEntries {
			return nil, fmt.Errorf("vmdisk: vhd logical offset %d beyond BAT (block index %d >= %d entries)", offset, blockIndex, v.maxEntries)
		}
		offsetInBlock := offset % v.blockSize
		chunk := v.blockSize - offsetInBlock
		if chunk > remaining {
			chunk = remaining
		}
		entryBytes, err := readAtBounded(v.file, v.batOffset+blockIndex*4, 4)
		if err != nil {
			return nil, err
		}
		entry := be32(entryBytes)
		var data []byte
		if entry == 0xFFFFFFFF {
			data = make([]byte, chunk) // unallocated block reads as zero
		} else {
			blockDataOffset := int64(entry)*512 + v.bitmapBytes
			data, err = readAtBounded(v.file, blockDataOffset+offsetInBlock, chunk)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, data...)
		offset += chunk
		remaining -= chunk
	}
	return out, nil
}

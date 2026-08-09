package vmdisk

import (
	"bytes"
	"fmt"
	"os"
)

// vhdxLogicalDisk maps a (non-differencing) VHDX's Block Allocation
// Table. Differencing VHDXs (which read unallocated blocks from a
// separate parent file) are explicitly not supported. Region/metadata
// checksums are not verified - this package only ever reads a bounded
// header region to find the partition table, so a corrupt-but-plausible
// header produces "cannot determine", not a wrong answer, because every
// offset/size read from it is still bounds-checked before use.
type vhdxLogicalDisk struct {
	file          *os.File
	batOffset     int64
	blockSize     int64
	virtualLength int64
}

const (
	vhdxRegionTableOffset       = 192 * 1024
	vhdxRegionTableCopyOffset   = 256 * 1024
	vhdxMetadataTableEntrySize  = 32
	vhdxRegionTableEntrySize    = 32
	vhdxBATStateFullyPresent    = 6
	vhdxBATStateMask            = 0x7
	vhdxBATFileOffsetShift      = 20
	vhdxBATFileOffsetGranularBy = 1 << 20 // block file offsets are 1 MiB-granular
)

// 16-byte on-disk (mixed-endian) encodings of the well-known VHDX GUIDs.
var (
	vhdxRegionBAT      = []byte{0x66, 0x77, 0xC2, 0x2D, 0x23, 0xF6, 0x00, 0x42, 0x9D, 0x64, 0x11, 0x5E, 0x9B, 0xFD, 0x4A, 0x08}
	vhdxRegionMetadata = []byte{0x06, 0xA2, 0x7C, 0x8B, 0x90, 0x47, 0x9A, 0x4B, 0xB8, 0xFE, 0x57, 0x5F, 0x05, 0x0F, 0x88, 0x6E}
	vhdxMetaFileParams = []byte{0x37, 0x67, 0xA1, 0xCA, 0x36, 0xFA, 0x43, 0x4D, 0xB3, 0xB6, 0x33, 0xF0, 0xAA, 0x44, 0xE7, 0x6B}
)

func newVHDXLogicalDisk(file *os.File) (*vhdxLogicalDisk, error) {
	batFileOffset, batLength, metaFileOffset, metaLength, err := vhdxRegions(file)
	if err != nil {
		return nil, err
	}
	blockSize, hasParent, err := vhdxFileParameters(file, metaFileOffset, metaLength)
	if err != nil {
		return nil, err
	}
	if hasParent {
		return nil, fmt.Errorf("%w: differencing vhdx (has a parent disk) is not supported", ErrCannotMapLogicalDisk)
	}
	if blockSize <= 0 || blockSize > maxMappedRegionSize {
		return nil, fmt.Errorf("%w: implausible vhdx block size=%d", ErrCannotMapLogicalDisk, blockSize)
	}
	if batLength <= 0 {
		return nil, fmt.Errorf("%w: empty vhdx BAT region", ErrCannotMapLogicalDisk)
	}
	return &vhdxLogicalDisk{file: file, batOffset: batFileOffset, blockSize: blockSize}, nil
}

// vhdxRegions locates the BAT and Metadata regions via the Region
// Table, trying the primary copy (192 KiB) then the backup (256 KiB).
func vhdxRegions(file *os.File) (batOffset, batLength, metaOffset, metaLength int64, err error) {
	for _, base := range []int64{vhdxRegionTableOffset, vhdxRegionTableCopyOffset} {
		header, readErr := readAtBounded(file, base, 16)
		if readErr != nil {
			continue
		}
		if !bytes.Equal(header[:4], []byte("regi")) {
			continue
		}
		entryCount := le32(header[8:12])
		if entryCount > 2048 { // 64 KiB region / 32-byte entries, generously bounded
			continue
		}
		var foundBAT, foundMeta bool
		for i := uint32(0); i < entryCount; i++ {
			entry, readErr := readAtBounded(file, base+16+int64(i)*vhdxRegionTableEntrySize, vhdxRegionTableEntrySize)
			if readErr != nil {
				return 0, 0, 0, 0, readErr
			}
			guid := entry[0:16]
			fileOffset := int64(le64(entry[16:24]))
			length := int64(le32(entry[24:28]))
			switch {
			case bytes.Equal(guid, vhdxRegionBAT):
				batOffset, batLength, foundBAT = fileOffset, length, true
			case bytes.Equal(guid, vhdxRegionMetadata):
				metaOffset, metaLength, foundMeta = fileOffset, length, true
			}
		}
		if foundBAT && foundMeta {
			return batOffset, batLength, metaOffset, metaLength, nil
		}
	}
	return 0, 0, 0, 0, fmt.Errorf("%w: vhdx region table did not yield BAT and Metadata regions", ErrCannotMapLogicalDisk)
}

// vhdxFileParameters reads the "File Parameters" metadata item (block
// size and the "has parent" flag) out of the Metadata region.
func vhdxFileParameters(file *os.File, metaOffset, metaLength int64) (blockSize int64, hasParent bool, err error) {
	if metaLength < 32 || metaLength > 4<<20 {
		return 0, false, fmt.Errorf("%w: implausible vhdx metadata region length=%d", ErrCannotMapLogicalDisk, metaLength)
	}
	header, err := readAtBounded(file, metaOffset, 12)
	if err != nil {
		return 0, false, err
	}
	if !bytes.Equal(header[:8], []byte("metadata")) {
		return 0, false, fmt.Errorf("%w: vhdx metadata region signature missing", ErrCannotMapLogicalDisk)
	}
	entryCount := be16LE(header[10:12])
	if entryCount > 2048 {
		return 0, false, fmt.Errorf("%w: implausible vhdx metadata entry count=%d", ErrCannotMapLogicalDisk, entryCount)
	}
	const vhdxMetadataHeaderSize = 32 // "metadata"(8) + reserved(2) + EntryCount(2) + reserved(20)
	for i := uint16(0); i < entryCount; i++ {
		entry, err := readAtBounded(file, metaOffset+vhdxMetadataHeaderSize+int64(i)*vhdxMetadataTableEntrySize, vhdxMetadataTableEntrySize)
		if err != nil {
			return 0, false, err
		}
		if !bytes.Equal(entry[0:16], vhdxMetaFileParams) {
			continue
		}
		itemOffset := int64(le32(entry[16:20]))
		item, err := readAtBounded(file, metaOffset+itemOffset, 8)
		if err != nil {
			return 0, false, err
		}
		size := int64(le32(item[0:4]))
		leaveFlags := le32(item[4:8])
		const hasParentBit = 1 << 1
		return size, leaveFlags&hasParentBit != 0, nil
	}
	return 0, false, fmt.Errorf("%w: vhdx metadata has no File Parameters item", ErrCannotMapLogicalDisk)
}

func (v *vhdxLogicalDisk) ReadLogical(offset, length int64) ([]byte, error) {
	out := make([]byte, 0, length)
	for remaining := length; remaining > 0; {
		blockIndex := offset / v.blockSize
		offsetInBlock := offset % v.blockSize
		chunk := v.blockSize - offsetInBlock
		if chunk > remaining {
			chunk = remaining
		}

		entryBytes, err := readAtBounded(v.file, v.batOffset+blockIndex*8, 8)
		if err != nil {
			return nil, err
		}
		entry := le64(entryBytes)
		state := entry & vhdxBATStateMask

		var data []byte
		if state != vhdxBATStateFullyPresent {
			data = make([]byte, chunk) // not present (or only partially): treated as zero
		} else {
			fileOffset := int64(entry>>vhdxBATFileOffsetShift) * vhdxBATFileOffsetGranularBy
			data, err = readAtBounded(v.file, fileOffset+offsetInBlock, chunk)
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

func be16LE(b []byte) uint16 { return uint16(b[0]) | uint16(b[1])<<8 }

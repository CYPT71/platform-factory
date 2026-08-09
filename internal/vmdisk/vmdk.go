package vmdisk

import (
	"fmt"
	"os"
)

// vmdkLogicalDisk walks the two-level Grain Directory / Grain Table
// mapping used by VMDK's binary "hosted sparse extent" format. The
// plain-text descriptor form (which references separate extent files
// by relative path) is intentionally not supported here - resolving it
// safely means validating cross-file path references, which is a
// separate, larger piece of work; see docs/legacy-vm-disk-boot.md.
type vmdkLogicalDisk struct {
	file          *os.File
	gdOffsetBytes int64 // Grain Directory offset, in bytes
	grainSectors  int64 // grain size, in 512-byte sectors
	grainBytes    int64 // grain size, in bytes
	gtEntries     int64 // numGTEsPerGT
}

const invalidVMDKOffset = 0xFFFFFFFFFFFFFFFF

func newVMDKLogicalDisk(file *os.File) (*vmdkLogicalDisk, error) {
	header, err := readAtBounded(file, 0, 512)
	if err != nil {
		return nil, fmt.Errorf("vmdisk: read vmdk header: %w", err)
	}
	if string(header[:4]) != "KDMV" {
		return nil, fmt.Errorf("%w: vmdk text-descriptor form is not supported for block mapping", ErrCannotMapLogicalDisk)
	}
	grainSectors := int64(le64(header[20:28]))
	gtEntries := int64(le32(header[44:48]))
	gdOffset := le64(header[56:64])
	rgdOffset := le64(header[48:56])
	if gdOffset == 0 || gdOffset == invalidVMDKOffset {
		gdOffset = rgdOffset
	}
	if gdOffset == 0 || gdOffset == invalidVMDKOffset {
		return nil, fmt.Errorf("%w: vmdk has no usable grain directory offset", ErrCannotMapLogicalDisk)
	}
	if grainSectors <= 0 || grainSectors*512 > maxMappedRegionSize || gtEntries <= 0 || gtEntries > (1<<20) {
		return nil, fmt.Errorf("%w: implausible vmdk grain size=%d or table entries=%d", ErrCannotMapLogicalDisk, grainSectors, gtEntries)
	}
	return &vmdkLogicalDisk{
		file: file, gdOffsetBytes: int64(gdOffset) * 512,
		grainSectors: grainSectors, grainBytes: grainSectors * 512, gtEntries: gtEntries,
	}, nil
}

func (v *vmdkLogicalDisk) ReadLogical(offset, length int64) ([]byte, error) {
	out := make([]byte, 0, length)
	for remaining := length; remaining > 0; {
		grainIndex := offset / v.grainBytes
		offsetInGrain := offset % v.grainBytes
		chunk := v.grainBytes - offsetInGrain
		if chunk > remaining {
			chunk = remaining
		}

		gtIndex := grainIndex / v.gtEntries
		entryIndex := grainIndex % v.gtEntries

		gdEntryBytes, err := readAtBounded(v.file, v.gdOffsetBytes+gtIndex*4, 4)
		if err != nil {
			return nil, err
		}
		gdEntry := le32(gdEntryBytes)

		var data []byte
		if gdEntry == 0 {
			data = make([]byte, chunk) // grain table not allocated: zero
		} else {
			gtEntryBytes, err := readAtBounded(v.file, int64(gdEntry)*512+entryIndex*4, 4)
			if err != nil {
				return nil, err
			}
			gtEntry := le32(gtEntryBytes)
			if gtEntry == 0 {
				data = make([]byte, chunk) // grain not allocated: zero
			} else {
				grainOffset := int64(gtEntry) * 512
				data, err = readAtBounded(v.file, grainOffset+offsetInGrain, chunk)
				if err != nil {
					return nil, err
				}
			}
		}
		out = append(out, data...)
		offset += chunk
		remaining -= chunk
	}
	return out, nil
}

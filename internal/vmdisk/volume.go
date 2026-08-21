package vmdisk

import (
	"bytes"
	"encoding/hex"
	"fmt"
)

// VolumeMap is a stable, filesystem-independent view of a disk's partition
// table. Entries are ordered exactly as they appear on disk; empty entries and
// the MBR extended-partition container are omitted.
type VolumeMap struct {
	Table   string   `json:"table"`
	Volumes []Volume `json:"volumes"`
}

// Volume describes a partition without reading or mounting its filesystem.
type Volume struct {
	Index             int    `json:"index"`
	Kind              string `json:"kind"`
	Type              string `json:"type"`
	StartBytes        uint64 `json:"start_bytes"`
	SizeBytes         uint64 `json:"size_bytes"`
	Bootable          bool   `json:"bootable"`
	Content           string `json:"content"`
	RequiresKey       bool   `json:"requires_key"`
	SignatureEvidence string `json:"signature_evidence,omitempty"`
}

// ScanVolumeMap returns a deterministic partition map using the same bounded
// logical-disk readers and corruption checks as ScanBootPartition.
func ScanVolumeMap(path string, format Format) (VolumeMap, error) {
	ld, closer, err := openLogicalDisk(path, format)
	if err != nil {
		return VolumeMap{}, err
	}
	defer closer.Close()
	window, err := ld.ReadLogical(0, partitionScanWindow)
	if err != nil {
		return VolumeMap{}, err
	}
	if len(window) < 512 || window[510] != 0x55 || window[511] != 0xaa {
		return VolumeMap{Table: "none", Volumes: []Volume{}}, nil
	}
	entries := mbrPartitionEntries(window)
	if err := validateMBRPartitions(entries); err != nil {
		return VolumeMap{}, err
	}
	for _, entry := range entries {
		if entry.typeByte == gptProtectiveMBRType {
			result, err := mapGPTVolumes(window)
			if err == nil {
				err = classifyVolumes(ld, &result)
			}
			return result, err
		}
	}
	result, err := mapMBRVolumes(ld, entries)
	if err == nil {
		err = classifyVolumes(ld, &result)
	}
	return result, err
}

func mapMBRVolumes(ld logicalDisk, entries [4]mbrEntry) (VolumeMap, error) {
	result := VolumeMap{Table: "mbr", Volumes: []Volume{}}
	for _, entry := range entries {
		if entry.sectors == 0 || isExtendedPartitionType(entry.typeByte) {
			continue
		}
		result.Volumes = append(result.Volumes, mbrVolume(len(result.Volumes), "primary", uint64(entry.startLBA), entry))
	}
	for primaryIndex, container := range entries {
		if !isExtendedPartitionType(container.typeByte) || container.sectors == 0 {
			continue
		}
		base, current := uint64(container.startLBA), uint64(container.startLBA)
		visited := map[uint64]bool{}
		terminated := false
		for logicalIndex := 0; logicalIndex < maxExtendedPartitions; logicalIndex++ {
			if visited[current] {
				return VolumeMap{}, fmt.Errorf("%w: extended partition %d contains an EBR cycle at LBA %d", ErrCorruptPartitionTable, primaryIndex, current)
			}
			visited[current] = true
			if current > uint64(^uint64(0)>>1)/512 {
				return VolumeMap{}, fmt.Errorf("%w: extended partition EBR offset overflows", ErrCorruptPartitionTable)
			}
			sector, err := ld.ReadLogical(int64(current*512), 512)
			if err != nil {
				return VolumeMap{}, err
			}
			if sector[510] != 0x55 || sector[511] != 0xaa {
				return VolumeMap{}, fmt.Errorf("%w: extended partition %d EBR %d has no boot signature", ErrCorruptPartitionTable, primaryIndex, logicalIndex)
			}
			logical := mbrPartitionEntries(sector)
			if err := validateEBR(logical); err != nil {
				return VolumeMap{}, err
			}
			if logical[0].sectors > 0 {
				absolute := current + uint64(logical[0].startLBA)
				result.Volumes = append(result.Volumes, mbrVolume(len(result.Volumes), "logical", absolute, logical[0]))
			}
			link := logical[1]
			if link.sectors == 0 {
				terminated = true
				break
			}
			if !isExtendedPartitionType(link.typeByte) {
				return VolumeMap{}, fmt.Errorf("%w: EBR link entry has non-extended type %#x", ErrCorruptPartitionTable, link.typeByte)
			}
			next := base + uint64(link.startLBA)
			if next < base {
				return VolumeMap{}, fmt.Errorf("%w: extended partition link overflows", ErrCorruptPartitionTable)
			}
			current = next
		}
		if !terminated {
			return VolumeMap{}, fmt.Errorf("%w: extended partition exceeds %d EBR entries", ErrCorruptPartitionTable, maxExtendedPartitions)
		}
	}
	return result, nil
}

func mbrVolume(index int, kind string, start uint64, entry mbrEntry) Volume {
	return Volume{Index: index, Kind: kind, Type: fmt.Sprintf("mbr:0x%02x", entry.typeByte), StartBytes: start * 512, SizeBytes: uint64(entry.sectors) * 512, Bootable: entry.bootFlag == 0x80, Content: "unknown"}
}

func mapGPTVolumes(window []byte) (VolumeMap, error) {
	if len(window) < 1024 || !bytes.Equal(window[512:520], []byte("EFI PART")) {
		return VolumeMap{}, fmt.Errorf("%w: protective MBR has no GPT header", ErrCorruptPartitionTable)
	}
	header := window[512:]
	start, count, size := int64(le64(header[72:80]))*512, le32(header[80:84]), le32(header[84:88])
	if count > maxGPTPartitionEntries || size < 128 || size > 512 {
		return VolumeMap{}, fmt.Errorf("%w: GPT partition array size is implausible", ErrCorruptPartitionTable)
	}
	end := start + int64(count)*int64(size)
	if start < 0 || end > int64(len(window)) {
		return VolumeMap{}, fmt.Errorf("%w: GPT partition array exceeds bounded scan window", ErrCorruptPartitionTable)
	}
	result := VolumeMap{Table: "gpt", Volumes: []Volume{}}
	type partitionRange struct{ start, length uint64 }
	var ranges []partitionRange
	for i := uint32(0); i < count; i++ {
		entry := window[start+int64(i)*int64(size) : start+int64(i+1)*int64(size)]
		if isZero(entry[:16]) {
			continue
		}
		first, last := le64(entry[32:40]), le64(entry[40:48])
		if first == 0 || last < first || last == ^uint64(0) {
			return VolumeMap{}, fmt.Errorf("%w: GPT entry %d has inconsistent geometry", ErrCorruptPartitionTable, i)
		}
		length := last - first + 1
		if first > ^uint64(0)/512 || length > ^uint64(0)/512 {
			return VolumeMap{}, fmt.Errorf("%w: GPT entry %d byte geometry overflows", ErrCorruptPartitionTable, i)
		}
		for j, previous := range ranges {
			if rangesOverlap(first, length, previous.start, previous.length) {
				return VolumeMap{}, fmt.Errorf("%w: GPT entries %d and %d overlap", ErrCorruptPartitionTable, j, i)
			}
		}
		ranges = append(ranges, partitionRange{first, length})
		result.Volumes = append(result.Volumes, Volume{Index: len(result.Volumes), Kind: "gpt", Type: "gpt:" + hex.EncodeToString(entry[:16]), StartBytes: first * 512, SizeBytes: length * 512, Bootable: bytes.Equal(entry[:16], gptESPTypeGUID) || le64(entry[48:56])&gptLegacyBIOSBootableBit != 0, Content: "unknown"})
	}
	return result, nil
}

// volumeSignatureWindow covers the furthest supported signature (Btrfs at
// 64 KiB + 64 bytes) while keeping total inspection bounded to 128 KiB per
// partition. No directory, inode or user data is read by this classifier.
const volumeSignatureWindow = 128 * 1024

func classifyVolumes(ld logicalDisk, volumeMap *VolumeMap) error {
	for i := range volumeMap.Volumes {
		volume := &volumeMap.Volumes[i]
		if volume.StartBytes > uint64(^uint64(0)>>1) {
			return fmt.Errorf("%w: volume %d offset cannot be represented", ErrCorruptPartitionTable, volume.Index)
		}
		length := volume.SizeBytes
		if length > volumeSignatureWindow {
			length = volumeSignatureWindow
		}
		window, err := ld.ReadLogical(int64(volume.StartBytes), int64(length))
		if err != nil {
			return fmt.Errorf("vmdisk: inspect volume %d signature: %w", volume.Index, err)
		}
		classifyVolumeSignature(volume, window)
	}
	return nil
}

func classifyVolumeSignature(volume *Volume, window []byte) {
	if len(window) >= 8 && bytes.Equal(window[:6], []byte{'L', 'U', 'K', 'S', 0xba, 0xbe}) {
		volume.Content = "luks"
		volume.RequiresKey = true
		volume.SignatureEvidence = fmt.Sprintf("LUKS%d header magic at volume offset 0", uint16(window[6])<<8|uint16(window[7]))
		return
	}
	if len(window) >= 11 && bytes.Equal(window[3:11], []byte("-FVE-FS-")) {
		volume.Content = "bitlocker"
		volume.RequiresKey = true
		volume.SignatureEvidence = "BitLocker FVE filesystem signature at volume offset 3"
		return
	}
	for sector := 0; sector < 4; sector++ {
		offset := sector * 512
		if len(window) >= offset+32 && bytes.Equal(window[offset:offset+8], []byte("LABELONE")) && bytes.Equal(window[offset+24:offset+32], []byte("LVM2 001")) {
			volume.Content = "lvm2-physical-volume"
			volume.SignatureEvidence = fmt.Sprintf("LVM2 LABELONE header at volume sector %d", sector)
			return
		}
	}
	for pageSize := 4096; pageSize <= 64*1024; pageSize *= 2 {
		if len(window) >= pageSize && bytes.Equal(window[pageSize-10:pageSize], []byte("SWAPSPACE2")) {
			volume.Content = "linux-swap"
			volume.SignatureEvidence = fmt.Sprintf("SWAPSPACE2 signature at %d-byte page boundary", pageSize)
			return
		}
	}
	if len(window) >= 1082 && window[1080] == 0x53 && window[1081] == 0xef {
		volume.Content = "unsupported-filesystem:ext2-ext3-ext4"
		volume.SignatureEvidence = "ext-family superblock magic at volume offset 1080"
		return
	}
	if len(window) >= 4 && bytes.Equal(window[:4], []byte("XFSB")) {
		volume.Content = "unsupported-filesystem:xfs"
		volume.SignatureEvidence = "XFS superblock magic at volume offset 0"
		return
	}
	if len(window) >= 0x10048 && bytes.Equal(window[0x10040:0x10048], []byte("_BHRfS_M")) {
		volume.Content = "unsupported-filesystem:btrfs"
		volume.SignatureEvidence = "Btrfs superblock magic at volume offset 65600"
		return
	}
	if len(window) >= 11 && bytes.Equal(window[3:11], []byte("NTFS    ")) {
		volume.Content = "unsupported-filesystem:ntfs"
		volume.SignatureEvidence = "NTFS OEM identifier at volume offset 3"
		return
	}
	if len(window) >= 90 && (bytes.Equal(window[54:62], []byte("FAT12   ")) || bytes.Equal(window[54:62], []byte("FAT16   ")) || bytes.Equal(window[82:90], []byte("FAT32   "))) {
		volume.Content = "unsupported-filesystem:fat"
		volume.SignatureEvidence = "FAT filesystem type identifier in boot sector"
	}
}

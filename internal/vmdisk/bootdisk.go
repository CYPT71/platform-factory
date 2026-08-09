package vmdisk

import "bytes"

// BootScan describes what ScanBootPartition found by reading a disk's
// partition table only - never a filesystem's content, and never more
// than partitionScanWindow bytes of the disk's logical address space.
type BootScan struct {
	Bootable bool
	Evidence string
}

// 16-byte on-disk (mixed-endian) encoding of the GPT EFI System
// Partition type GUID (C12A7328-F81F-11D2-BA4B-00A0C93EC93B).
var gptESPTypeGUID = []byte{0x28, 0x73, 0x2A, 0xC1, 0x1F, 0xF8, 0xD2, 0x11, 0xBA, 0x4B, 0x00, 0xA0, 0xC9, 0x3E, 0xC9, 0x3B}

const gptProtectiveMBRType = 0xEE
const gptLegacyBIOSBootableBit = 1 << 2

// maxGPTPartitionEntries bounds how many partition entries
// ScanBootPartition will inspect, regardless of what a (possibly
// corrupt) header claims - large enough for every real-world GPT disk
// (128 is the near-universal default), small enough to stay well within
// partitionScanWindow.
const maxGPTPartitionEntries = 256

// ScanBootPartition opens path (already identified as format by
// Detect), reads only its partition-table region via the format's
// logical-disk mapping, and reports whether it carries positive
// evidence of being a boot disk: an MBR active/bootable partition flag,
// a GPT EFI System Partition, or a GPT "Legacy BIOS Bootable"
// attribute. A disk with a valid but non-bootable partition table (or
// no partition table at all) is a determinate, non-error result:
// Bootable=false. ScanBootPartition never mounts or reads filesystem
// content.
func ScanBootPartition(path string, format Format) (BootScan, error) {
	ld, closer, err := openLogicalDisk(path, format)
	if err != nil {
		return BootScan{}, err
	}
	defer closer.Close()

	window, err := ld.ReadLogical(0, partitionScanWindow)
	if err != nil {
		return BootScan{}, err
	}

	if len(window) < 512 || window[510] != 0x55 || window[511] != 0xaa {
		return BootScan{Bootable: false, Evidence: "no MBR boot signature at LBA0"}, nil
	}

	entries := mbrPartitionEntries(window)
	for _, entry := range entries {
		if entry.typeByte == gptProtectiveMBRType {
			return scanGPT(window)
		}
	}
	for i, entry := range entries {
		if entry.bootFlag == 0x80 {
			return BootScan{Bootable: true, Evidence: "MBR active/bootable flag on partition entry " + itoa(i)}, nil
		}
	}
	return BootScan{Bootable: false, Evidence: "valid MBR, no partition marked active/bootable"}, nil
}

type mbrEntry struct {
	bootFlag byte
	typeByte byte
}

func mbrPartitionEntries(window []byte) [4]mbrEntry {
	var entries [4]mbrEntry
	for i := 0; i < 4; i++ {
		off := 446 + i*16
		entries[i] = mbrEntry{bootFlag: window[off], typeByte: window[off+4]}
	}
	return entries
}

func scanGPT(window []byte) (BootScan, error) {
	if len(window) < 1024 || !bytes.Equal(window[512:520], []byte("EFI PART")) {
		return BootScan{Bootable: false, Evidence: "protective MBR present but GPT header signature missing at LBA1"}, nil
	}
	header := window[512:]
	partitionEntryLBA := int64(le64(header[72:80]))
	entryCount := le32(header[80:84])
	entrySize := le32(header[84:88])
	if entryCount > maxGPTPartitionEntries || entrySize < 128 || entrySize > 512 {
		return BootScan{Bootable: false, Evidence: "GPT header present but partition array size is implausible"}, nil
	}
	arrayStart := partitionEntryLBA * 512
	arrayEnd := arrayStart + int64(entryCount)*int64(entrySize)
	if arrayStart < 0 || arrayEnd > int64(len(window)) {
		// The partition array lies outside the bounded window this
		// package reads; refusing rather than reading further is the
		// fail-closed choice here (see partitionScanWindow).
		return BootScan{Bootable: false, Evidence: "GPT partition array extends beyond the bounded scan window"}, nil
	}
	for i := uint32(0); i < entryCount; i++ {
		entry := window[arrayStart+int64(i)*int64(entrySize) : arrayStart+int64(i+1)*int64(entrySize)]
		typeGUID := entry[0:16]
		if isZero(typeGUID) {
			continue
		}
		if bytes.Equal(typeGUID, gptESPTypeGUID) {
			return BootScan{Bootable: true, Evidence: "GPT EFI System Partition present (entry " + itoa(int(i)) + ")"}, nil
		}
		attributes := le64(entry[48:56])
		if attributes&gptLegacyBIOSBootableBit != 0 {
			return BootScan{Bootable: true, Evidence: "GPT Legacy BIOS Bootable attribute set (entry " + itoa(int(i)) + ")"}, nil
		}
	}
	return BootScan{Bootable: false, Evidence: "valid GPT, no EFI System Partition or Legacy BIOS Bootable attribute found"}, nil
}

func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

package vmdisk

import (
	"testing"
)

func writeRawDiskWithMBR(t *testing.T, active bool) string {
	t.Helper()
	buf := make([]byte, 4096)
	off := 446
	if active {
		buf[off] = 0x80
	}
	buf[off+4] = 0x83 // Linux partition type, not GPT protective
	buf[510], buf[511] = 0x55, 0xaa
	path := writeTemp(t, "mbr.raw", buf)
	return path
}

func TestScanBootPartitionMBRActive(t *testing.T) {
	path := writeRawDiskWithMBR(t, true)
	scan, err := ScanBootPartition(path, FormatRAW)
	if err != nil {
		t.Fatalf("ScanBootPartition: %v", err)
	}
	if !scan.Bootable {
		t.Fatalf("scan=%+v, want Bootable", scan)
	}
}

func TestScanBootPartitionMBRNotActive(t *testing.T) {
	path := writeRawDiskWithMBR(t, false)
	scan, err := ScanBootPartition(path, FormatRAW)
	if err != nil {
		t.Fatalf("ScanBootPartition: %v", err)
	}
	if scan.Bootable {
		t.Fatalf("scan=%+v, want not Bootable", scan)
	}
}

func TestScanBootPartitionNoMBRSignature(t *testing.T) {
	buf := make([]byte, 4096)
	path := writeTemp(t, "nosig.raw", buf)
	scan, err := ScanBootPartition(path, FormatRAW)
	if err != nil {
		t.Fatalf("ScanBootPartition: %v", err)
	}
	if scan.Bootable {
		t.Fatalf("scan=%+v, want not Bootable", scan)
	}
}

func buildGPTDisk(t *testing.T, partitionTypeGUID []byte, attributes uint64) string {
	t.Helper()
	buf := make([]byte, 1024+128) // LBA0 (MBR) + LBA1 (GPT header) + one partition entry at LBA2
	// Protective MBR
	buf[446+4] = gptProtectiveMBRType
	buf[510], buf[511] = 0x55, 0xaa
	// GPT header at LBA1 (byte offset 512)
	header := buf[512:1024]
	copy(header[0:8], "EFI PART")
	putLE64(header, 72, 2) // PartitionEntryLBA = 2
	putLE32(header, 80, 1) // NumberOfPartitionEntries = 1
	putLE32(header, 84, 128)
	// Partition entry at LBA2 (byte offset 1024)
	entry := buf[1024:1152]
	if partitionTypeGUID != nil {
		copy(entry[0:16], partitionTypeGUID)
	}
	putLE64(entry, 48, attributes)
	return writeTemp(t, "gpt.raw", buf)
}

func TestScanBootPartitionGPTEFISystemPartition(t *testing.T) {
	path := buildGPTDisk(t, gptESPTypeGUID, 0)
	scan, err := ScanBootPartition(path, FormatRAW)
	if err != nil {
		t.Fatalf("ScanBootPartition: %v", err)
	}
	if !scan.Bootable {
		t.Fatalf("scan=%+v, want Bootable (ESP)", scan)
	}
}

func TestScanBootPartitionGPTLegacyBIOSBootableAttribute(t *testing.T) {
	linuxGUID := make([]byte, 16)
	linuxGUID[0] = 0x01 // any non-zero, non-ESP type GUID
	path := buildGPTDisk(t, linuxGUID, gptLegacyBIOSBootableBit)
	scan, err := ScanBootPartition(path, FormatRAW)
	if err != nil {
		t.Fatalf("ScanBootPartition: %v", err)
	}
	if !scan.Bootable {
		t.Fatalf("scan=%+v, want Bootable (legacy BIOS bootable attribute)", scan)
	}
}

func TestScanBootPartitionGPTNoBootableEvidence(t *testing.T) {
	linuxGUID := make([]byte, 16)
	linuxGUID[0] = 0x01
	path := buildGPTDisk(t, linuxGUID, 0)
	scan, err := ScanBootPartition(path, FormatRAW)
	if err != nil {
		t.Fatalf("ScanBootPartition: %v", err)
	}
	if scan.Bootable {
		t.Fatalf("scan=%+v, want not Bootable", scan)
	}
}

func TestScanBootPartitionGPTEmptyPartitionEntrySkipped(t *testing.T) {
	// A GPT header claiming an entry that is actually all-zero (unused)
	// must not be mistaken for anything.
	path := buildGPTDisk(t, nil, 0)
	scan, err := ScanBootPartition(path, FormatRAW)
	if err != nil {
		t.Fatalf("ScanBootPartition: %v", err)
	}
	if scan.Bootable {
		t.Fatalf("scan=%+v, want not Bootable", scan)
	}
}

func TestScanBootPartitionPropagatesLogicalDiskErrors(t *testing.T) {
	path := writeTemp(t, "unmappable.iso", make([]byte, 4096))
	if _, err := ScanBootPartition(path, FormatISO); err == nil {
		t.Fatal("expected an error for a format with no logical-disk mapping")
	}
}

func TestScanBootPartitionOnDynamicVHD(t *testing.T) {
	// Proves ScanBootPartition works through a real block-mapping layer,
	// not just on RAW: block 0's logical content (not file offset 0,
	// which holds the Dynamic Disk Header) is an MBR with an active
	// partition.
	mbr := make([]byte, 4096)
	mbr[446] = 0x80
	mbr[446+4] = 0x83
	mbr[510], mbr[511] = 0x55, 0xaa
	vhdFile, _ := buildDynamicVHD(t, mbr)
	scan, err := ScanBootPartition(vhdFile.Name(), FormatVHD)
	if err != nil {
		t.Fatalf("ScanBootPartition: %v", err)
	}
	if !scan.Bootable {
		t.Fatalf("scan=%+v, want Bootable (via VHD block mapping)", scan)
	}
}

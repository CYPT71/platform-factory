package vmdisk

import (
	"os"
	"testing"
)

func TestScanVolumeMapMBRStableOrder(t *testing.T) {
	buf := make([]byte, 4096)
	buf[446], buf[450] = 0x80, 0x83
	putLE32(buf, 446+8, 1)
	putLE32(buf, 446+12, 1)
	buf[510], buf[511] = 0x55, 0xaa
	path := writeTemp(t, "volume-mbr.raw", buf)
	m, err := ScanVolumeMap(path, FormatRAW)
	if err != nil {
		t.Fatal(err)
	}
	if m.Table != "mbr" || len(m.Volumes) != 1 {
		t.Fatalf("unexpected map: %#v", m)
	}
	v := m.Volumes[0]
	if v.Index != 0 || v.Kind != "primary" || v.Type != "mbr:0x83" || v.StartBytes != 512 || v.SizeBytes != 512 || !v.Bootable {
		t.Fatalf("unexpected volume: %#v", v)
	}
}

func TestScanVolumeMapGPTStableOrder(t *testing.T) {
	path := buildGPTDisk(t, gptESPTypeGUID, 0)
	m, err := ScanVolumeMap(path, FormatRAW)
	if err != nil {
		t.Fatal(err)
	}
	if m.Table != "gpt" || len(m.Volumes) != 1 || m.Volumes[0].Index != 0 || !m.Volumes[0].Bootable {
		t.Fatalf("unexpected map: %#v", m)
	}
}

func TestScanVolumeMapExtendedUsesAbsoluteOffsets(t *testing.T) {
	buf := make([]byte, 16*512)
	buf[446+4] = 0x0f
	putLE32(buf, 446+8, 1)
	putLE32(buf, 446+12, 10)
	buf[510], buf[511] = 0x55, 0xaa
	ebr := buf[512:1024]
	ebr[446+4] = 0x83
	putLE32(ebr, 446+8, 1)
	putLE32(ebr, 446+12, 2)
	ebr[510], ebr[511] = 0x55, 0xaa
	path := writeTemp(t, "volume-extended.raw", buf)
	m, err := ScanVolumeMap(path, FormatRAW)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Volumes) != 1 || m.Volumes[0].Kind != "logical" || m.Volumes[0].StartBytes != 2*512 {
		t.Fatalf("unexpected map: %#v", m)
	}
}

func TestScanVolumeMapRejectsOverlappingGPT(t *testing.T) {
	buf := make([]byte, 1280)
	buf[446+4] = gptProtectiveMBRType
	buf[510], buf[511] = 0x55, 0xaa
	header := buf[512:1024]
	copy(header, "EFI PART")
	putLE64(header, 72, 2)
	putLE32(header, 80, 2)
	putLE32(header, 84, 128)
	for i, geometry := range [][2]uint64{{2048, 4095}, {4095, 8191}} {
		entry := buf[1024+i*128 : 1024+(i+1)*128]
		entry[0] = byte(i + 1)
		putLE64(entry, 32, geometry[0])
		putLE64(entry, 40, geometry[1])
	}
	path := writeTemp(t, "volume-overlap-gpt.raw", buf)
	if _, err := ScanVolumeMap(path, FormatRAW); err == nil {
		t.Fatal("expected overlapping GPT partitions to fail")
	}
}

func TestScanVolumeMapDetectsLVM2Signature(t *testing.T) {
	path := signatureDisk(t, 4096)
	data := make([]byte, 32)
	copy(data, "LABELONE")
	copy(data[24:], "LVM2 001")
	writeAt(t, path, 512+512, data)
	assertVolumeContent(t, path, "lvm2-physical-volume", false)
}

func TestScanVolumeMapDetectsLUKSAndRequiresKey(t *testing.T) {
	path := signatureDisk(t, 4096)
	writeAt(t, path, 512, []byte{'L', 'U', 'K', 'S', 0xba, 0xbe, 0, 2})
	assertVolumeContent(t, path, "luks", true)
}

func TestScanVolumeMapDetectsBitLockerAndRequiresKey(t *testing.T) {
	path := signatureDisk(t, 4096)
	writeAt(t, path, 512, []byte{0xeb, 0x58, 0x90, '-', 'F', 'V', 'E', '-', 'F', 'S', '-'})
	assertVolumeContent(t, path, "bitlocker", true)
}

func TestScanVolumeMapDetectsSwapSignature(t *testing.T) {
	path := signatureDisk(t, 8192)
	writeAt(t, path, 512+4096-10, []byte("SWAPSPACE2"))
	assertVolumeContent(t, path, "linux-swap", false)
}

func TestScanVolumeMapReportsUnsupportedFilesystemSignatures(t *testing.T) {
	tests := []struct {
		name    string
		offset  int
		magic   []byte
		content string
	}{
		{name: "ext", offset: 1080, magic: []byte{0x53, 0xef}, content: "unsupported-filesystem:ext2-ext3-ext4"},
		{name: "xfs", offset: 0, magic: []byte("XFSB"), content: "unsupported-filesystem:xfs"},
		{name: "btrfs", offset: 0x10040, magic: []byte("_BHRfS_M"), content: "unsupported-filesystem:btrfs"},
		{name: "ntfs", offset: 3, magic: []byte("NTFS    "), content: "unsupported-filesystem:ntfs"},
		{name: "fat32", offset: 82, magic: []byte("FAT32   "), content: "unsupported-filesystem:fat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := signatureDisk(t, volumeSignatureWindow)
			writeAt(t, path, 512+tt.offset, tt.magic)
			assertVolumeContent(t, path, tt.content, false)
		})
	}
}

func signatureDisk(t *testing.T, volumeBytes uint32) string {
	t.Helper()
	buf := make([]byte, 512+volumeBytes)
	buf[446+4] = 0x83
	putLE32(buf, 446+8, 1)
	putLE32(buf, 446+12, volumeBytes/512)
	buf[510], buf[511] = 0x55, 0xaa
	return writeTemp(t, "volume-signature.raw", buf)
}

func writeAt(t *testing.T, path string, offset int, value []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteAt(value, int64(offset)); err != nil {
		t.Fatal(err)
	}
}

func assertVolumeContent(t *testing.T, path, content string, requiresKey bool) {
	t.Helper()
	m, err := ScanVolumeMap(path, FormatRAW)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Volumes) != 1 || m.Volumes[0].Content != content || m.Volumes[0].RequiresKey != requiresKey || m.Volumes[0].SignatureEvidence == "" {
		t.Fatalf("unexpected signature result: %#v", m)
	}
}

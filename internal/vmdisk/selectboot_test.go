package vmdisk

import (
	"strings"
	"testing"
)

func writeRawOSDisk(t *testing.T, name string) string {
	t.Helper()
	buf := make([]byte, 4096)
	buf[446] = 0x80 // active/bootable
	buf[446+4] = 0x83
	buf[510], buf[511] = 0x55, 0xaa
	return writeTemp(t, name, buf)
}

func writeRawDataDisk(t *testing.T, name string) string {
	t.Helper()
	// A valid, partitioned but non-bootable RAW disk - the common shape
	// of a secondary data volume.
	buf := make([]byte, 4096)
	buf[446+4] = 0x83 // a partition exists...
	buf[510], buf[511] = 0x55, 0xaa
	// ...but no active/bootable flag on it.
	return writeTemp(t, name, buf)
}

func TestSelectBootDiskSingleBootableDiskWins(t *testing.T) {
	osDisk := writeRawOSDisk(t, "os.raw")
	dataDisk := writeRawDataDisk(t, "data.raw")
	idx, disks, err := SelectBootDisk([]string{dataDisk, osDisk}, "")
	if err != nil {
		t.Fatalf("SelectBootDisk: %v", err)
	}
	if idx != 1 {
		t.Fatalf("idx=%d, want 1 (os disk)", idx)
	}
	if len(disks) != 2 || disks[0].Format != FormatRAW || disks[1].Format != FormatRAW {
		t.Fatalf("disks=%+v", disks)
	}
}

func TestSelectBootDiskAmbiguousWhenMultipleBootable(t *testing.T) {
	first := writeRawOSDisk(t, "first.raw")
	second := writeRawOSDisk(t, "second.raw")
	_, _, err := SelectBootDisk([]string{first, second}, "")
	if err == nil {
		t.Fatal("expected an error when multiple disks are bootable")
	}
	if !strings.Contains(err.Error(), "--boot-disk") {
		t.Fatalf("err = %v, want a --boot-disk hint", err)
	}
}

func TestSelectBootDiskAmbiguousWhenNoneBootable(t *testing.T) {
	first := writeRawDataDisk(t, "first.raw")
	second := writeRawDataDisk(t, "second.raw")
	_, _, err := SelectBootDisk([]string{first, second}, "")
	if err == nil {
		t.Fatal("expected an error when no disk is bootable")
	}
}

func TestSelectBootDiskExplicitOverrideWins(t *testing.T) {
	first := writeRawDataDisk(t, "first.raw")
	second := writeRawDataDisk(t, "second.raw")
	idx, _, err := SelectBootDisk([]string{first, second}, second)
	if err != nil {
		t.Fatalf("SelectBootDisk: %v", err)
	}
	if idx != 1 {
		t.Fatalf("idx=%d, want 1 (override)", idx)
	}
}

func TestSelectBootDiskExplicitOverrideMustMatchAPath(t *testing.T) {
	first := writeRawDataDisk(t, "first.raw")
	_, _, err := SelectBootDisk([]string{first}, "/nonexistent/path")
	if err == nil {
		t.Fatal("expected an error for a --boot-disk that doesn't match any given disk")
	}
}

func TestSelectBootDiskSingleDiskNeedsNoScanEvidence(t *testing.T) {
	// A lone disk is the boot disk by definition - even one this package
	// cannot boot-scan at all, since there is no other candidate to be
	// ambiguous against.
	dataDisk := writeRawDataDisk(t, "only.raw")
	idx, disks, err := SelectBootDisk([]string{dataDisk}, "")
	if err != nil {
		t.Fatalf("SelectBootDisk: %v", err)
	}
	if idx != 0 || len(disks) != 1 {
		t.Fatalf("idx=%d disks=%+v", idx, disks)
	}
}

func TestSelectBootDiskRequiresAtLeastOneDisk(t *testing.T) {
	if _, _, err := SelectBootDisk(nil, ""); err == nil {
		t.Fatal("expected an error for zero disks")
	}
}

func TestSelectBootDiskUnscannableDiskDoesNotBlockAConfirmedOne(t *testing.T) {
	buf := make([]byte, 16*2048+2048)
	copy(buf[16*2048+1:], []byte("CD001"))
	iso := writeTemp(t, "install.iso", buf)
	osDisk := writeRawOSDisk(t, "os.raw")
	// The ISO can't be boot-scanned (no logical-disk mapping for ISO
	// today), but the RAW disk shows unambiguous, confirmed boot
	// evidence - one confirmed signal is enough even when a second,
	// unrelated disk's bootability is simply unknown rather than
	// contradictory.
	idx, disks, err := SelectBootDisk([]string{iso, osDisk}, "")
	if err != nil {
		t.Fatalf("SelectBootDisk: %v", err)
	}
	if idx != 1 {
		t.Fatalf("idx=%d, want 1 (the confirmed RAW OS disk)", idx)
	}
	if disks[0].BootScan != nil {
		t.Fatalf("expected the ISO's BootScan to be nil, got %+v", disks[0].BootScan)
	}
	if disks[0].ScanError == "" {
		t.Fatal("expected a non-empty ScanError explaining why the ISO wasn't scanned")
	}
}

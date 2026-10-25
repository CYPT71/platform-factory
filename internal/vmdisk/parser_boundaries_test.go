package vmdisk

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"testing"
)

func ntfsMinimalAttributeRecord(kind uint32, nonresident byte) []byte {
	record := make([]byte, 80)
	copy(record, "FILE")
	binary.LittleEndian.PutUint16(record[20:22], 56)
	binary.LittleEndian.PutUint16(record[22:24], 1)
	binary.LittleEndian.PutUint32(record[24:28], 80)
	binary.LittleEndian.PutUint32(record[56:60], kind)
	binary.LittleEndian.PutUint32(record[60:64], 16)
	record[64] = nonresident
	binary.LittleEndian.PutUint32(record[72:76], 0xffffffff)
	return record
}
func TestNTFSRejectsTruncatedUsedAttributesWithoutPanicking(t *testing.T) {
	r := &ntfsReader{clusterSize: 512, totalClusters: 100}
	if _, _, err := r.mftRuns(ntfsMinimalAttributeRecord(0x80, 1)); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatalf("truncated MFT data: %v", err)
	}
	if _, _, err := r.parseRecord(ntfsMinimalAttributeRecord(0x30, 0), 6); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatalf("truncated filename: %v", err)
	}
	for size := 0; size < 28; size++ {
		if _, _, err := r.parseRecord(make([]byte, size), 6); !errors.Is(err, ErrCorruptFilesystem) {
			t.Fatalf("truncated record %d: %v", size, err)
		}
	}
}

type countingLogical struct{ calls int }

func (d *countingLogical) ReadLogical(offset, length int64) ([]byte, error) {
	d.calls++
	if offset < 0 || length < 0 || length > 65536 {
		return nil, fmt.Errorf("invalid read")
	}
	return make([]byte, length), nil
}
func TestFilesystemReadersRejectEscapesBudgetUnderflowAndOffsetOverflow(t *testing.T) {
	for _, family := range []string{"ext", "ntfs", "xfs", "btrfs"} {
		t.Run(family, func(t *testing.T) {
			for _, test := range []struct{ start, size, budget, offset, length uint64 }{
				{0, 100, 100, 101, 1}, {0, 100, 100, 99, 2}, {0, 100, 1, 0, 2},
				{^uint64(0) - 1, 100, 100, 3, 1}, {uint64(1) << 63, 100, 100, 0, 1},
			} {
				disk := &countingLogical{}
				volume := Volume{StartBytes: test.start, SizeBytes: test.size}
				var err error
				switch family {
				case "ext":
					r := &extReader{ld: disk, volume: volume, bytesLimit: test.budget}
					_, err = r.read(test.offset, test.length)
				case "ntfs":
					r := &ntfsReader{ld: disk, volume: volume, bytesLimit: test.budget}
					_, err = r.read(test.offset, test.length)
				case "xfs":
					r := &xfsReader{ld: disk, volume: volume, bytesLimit: test.budget}
					_, err = r.read(test.offset, test.length)
				case "btrfs":
					r := &btrfsReader{ld: disk, volume: volume, bytesLimit: test.budget}
					_, err = r.raw(test.offset, test.length)
				}
				if !errors.Is(err, ErrCorruptFilesystem) || disk.calls != 0 {
					t.Fatalf("unconfined read %+v: %v, disk calls %d", test, err, disk.calls)
				}
			}
		})
	}
}
func TestFilesystemSuperblocksFailClosedOnCorruptGeometry(t *testing.T) {
	families := []struct {
		name    string
		build   func(*testing.T) (string, Volume)
		inspect func(string, Format, Volume) (FilesystemInventory, error)
		base    int
		offsets []int
	}{
		{"ext", func(t *testing.T) (string, Volume) { return buildExtFixture(t, "ext4", false) }, InspectExtFilesystem, 512 + 1024, []int{56, 24, 0, 4, 32, 40, 88, 96, 254}},
		{"ntfs", func(t *testing.T) (string, Volume) { return buildNTFSFixture(t, false) }, InspectNTFSFilesystem, 512, []int{3, 11, 13, 40, 48, 64, 510}},
		{"xfs", func(t *testing.T) (string, Volume) { return buildXFSFixture(t, false, false) }, InspectXFSFilesystem, 0, []int{0, 4, 8, 56, 84, 88, 104, 106, 123, 124, 192}},
		{"btrfs", buildBtrfsFixture, InspectBtrfsFilesystem, btrfsSuperOffset, []int{64, 48, 148, 198, 199, 160, 819}},
	}
	for _, family := range families {
		t.Run(family.name, func(t *testing.T) {
			path, volume := family.build(t)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, offset := range family.offsets {
				t.Run(fmt.Sprint(offset), func(t *testing.T) {
					raw := append([]byte(nil), original...)
					for i := 0; i < 4 && family.base+offset+i < len(raw); i++ {
						raw[family.base+offset+i] = 0xff
					}
					if (family.name == "ext" && (offset == 0 || offset == 32 || offset == 40)) || (family.name == "xfs" && (offset == 84 || offset == 88)) {
						for i := 0; i < 4; i++ {
							raw[family.base+offset+i] = 0
						}
					}
					if family.name == "ext" && offset == 254 {
						binary.LittleEndian.PutUint32(raw[family.base+96:family.base+100], 0x80)
					}
					if err := os.WriteFile(path, raw, 0600); err != nil {
						t.Fatal(err)
					}
					if _, err := family.inspect(path, FormatRAW, volume); err == nil {
						t.Fatalf("corrupt geometry at %d accepted", offset)
					}
				})
			}
		})
	}
}
func TestNTFSRunlistRefusesMalformedRunsAndSupportsNegativeDeltas(t *testing.T) {
	for _, raw := range [][]byte{nil, {0}, {0x01, 1, 0}, {0x11, 0, 1, 0}, {0x11, 1, 0xff, 0}, {0x11, 200, 99, 0}, {0x11, 1, 1}} {
		if _, err := parseNTFSRunlist(raw, 512, 100); !errors.Is(err, ErrCorruptFilesystem) {
			t.Fatalf("accepted %x: %v", raw, err)
		}
	}
	runs, err := parseNTFSRunlist([]byte{0x11, 1, 5, 0x11, 1, 0xff, 0}, 512, 100)
	if err != nil || len(runs) != 2 || runs[1].physical != 4*512 {
		t.Fatalf("negative LCN delta: %v %+v", err, runs)
	}
	if _, err := (&ntfsReader{}).readMFT(nil, 0, 1); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatal("MFT hole accepted")
	}
}
func TestNTFSAttributesAndParentCycles(t *testing.T) {
	for _, raw := range [][]byte{nil, make([]byte, 28), []byte("FILE"), ntfsMinimalAttributeRecord(0x10, 0)[:70]} {
		if _, err := ntfsAttributes(raw); !errors.Is(err, ErrCorruptFilesystem) {
			t.Fatalf("invalid record accepted: %x", raw)
		}
	}
	if _, err := ntfsRecordPath(6, map[uint64]ntfsRecord{6: {name: "a", parent: 7}, 7: {name: "b", parent: 6}}); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatal("parent cycle accepted")
	}
	if path, err := ntfsRecordPath(6, map[uint64]ntfsRecord{}); err != nil || path != "" {
		t.Fatalf("unknown parent: %q %v", path, err)
	}
}

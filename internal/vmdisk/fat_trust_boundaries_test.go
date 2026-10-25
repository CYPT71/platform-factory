package vmdisk

import (
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

func TestFATRejectsMalformedGeometryBeforeDirectoryReads(t *testing.T) {
	disk, volume := buildFATFixture(t, 12, false)
	original, err := os.ReadFile(disk)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func([]byte){
		"signature":           func(b []byte) { b[510] = 0 },
		"sector size":         func(b []byte) { binary.LittleEndian.PutUint16(b[11:], 513) },
		"cluster size":        func(b []byte) { b[13] = 3 },
		"missing reservation": func(b []byte) { binary.LittleEndian.PutUint16(b[14:], 0) },
		"missing FAT":         func(b []byte) { b[16] = 0 },
		"outside volume":      func(b []byte) { binary.LittleEndian.PutUint16(b[19:], 65535) },
		"no data region":      func(b []byte) { binary.LittleEndian.PutUint16(b[19:], 1) },
	} {
		t.Run(name, func(t *testing.T) {
			data := append(memoryLogical(nil), original...)
			mutate(data[volume.StartBytes:])
			reader := fatReader{ld: data, volume: volume, bytesLimit: maxInventoryLogicalBytes}
			if err := reader.loadBootSector(); !errors.Is(err, ErrCorruptFilesystem) {
				t.Fatalf("malformed geometry: %v", err)
			}
		})
	}
	reader := fatReader{ld: memoryLogical(original), volume: volume, bytesLimit: maxInventoryLogicalBytes}
	if err := reader.loadBootSector(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.clusterData(reader.clusterCount + 2); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatal("outside cluster accepted")
	}
	for _, value := range []uint32{0, 1, 0xff7, uint32(reader.clusterCount) + 2} {
		raw := append(memoryLogical(nil), original...)
		table := raw[volume.StartBytes+reader.fatStart:]
		setFATEntry(table, 12, 3, value)
		reader.ld = raw
		if _, _, err := reader.nextCluster(3); !errors.Is(err, ErrCorruptFilesystem) {
			t.Fatalf("bad odd FAT12 chain %#x accepted: %v", value, err)
		}
	}
	reader.bytesLimit = 1
	if _, err := reader.read(0, 2); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatal("read budget escaped")
	}
	reader.bytesLimit = 1024
	reader.volume = Volume{StartBytes: ^uint64(0) - 1, SizeBytes: 512}
	if _, err := reader.read(4, 1); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatal("absolute read offset overflow accepted")
	}
}

func TestFATDirectoryIgnoresMetadataAndRejectsUnsafeEntries(t *testing.T) {
	newReader := func() *fatReader {
		return &fatReader{clusterCount: 10, filesLimit: 2, visitedDirs: map[uint32]bool{}, inventory: &FilesystemInventory{}}
	}
	data := make([]byte, 5*32)
	data[0] = 0xe5
	writeFATShortEntry(data[32:64], "VOLUME     ", 0x08, 0, 0)
	writeFATShortEntry(data[64:96], ".          ", 0x10, 0, 0)
	writeFATShortEntry(data[96:128], "GOOD    TXT", 0x20, 2, 5)
	reader := newReader()
	if err := reader.walkDirectoryData(data, "/"); err != nil {
		t.Fatal(err)
	}
	if len(reader.inventory.Files) != 1 || reader.inventory.Files[0].Path != "/GOOD.TXT" {
		t.Fatalf("metadata appeared in inventory: %+v", reader.inventory.Files)
	}
	for name, entry := range map[string][]byte{
		"truncated":                 make([]byte, 31),
		"unsafe filename":           func() []byte { b := make([]byte, 32); writeFATShortEntry(b, "BAD/NAME   ", 0x20, 2, 1); return b }(),
		"invalid file cluster":      func() []byte { b := make([]byte, 32); writeFATShortEntry(b, "BAD     TXT", 0x20, 0, 1); return b }(),
		"invalid directory cluster": func() []byte { b := make([]byte, 32); writeFATShortEntry(b, "BAD        ", 0x10, 0, 0); return b }(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := newReader().walkDirectoryData(entry, "/"); !errors.Is(err, ErrCorruptFilesystem) {
				t.Fatalf("unsafe entry: %v", err)
			}
		})
	}
	limited := newReader()
	limited.filesLimit = 0
	if err := limited.walkDirectoryData(data, "/"); err != nil || !limited.inventory.Truncated {
		t.Fatal("bounded inventory did not report truncation")
	}
	cyclic := newReader()
	cyclic.visitedDirs[2] = true
	if err := cyclic.walkClusterDirectory(2, "/"); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatal("directory revisit accepted")
	}
}

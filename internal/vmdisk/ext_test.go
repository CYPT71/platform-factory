package vmdisk

import (
	"encoding/binary"
	"errors"
	"testing"
)

func TestInspectExtFilesystemInventoriesMetadata(t *testing.T) {
	for _, filesystem := range []string{"ext2", "ext3", "ext4"} {
		t.Run(filesystem, func(t *testing.T) {
			path, volume := buildExtFixture(t, filesystem, false)
			inventory, err := InspectExtFilesystem(path, FormatRAW, volume)
			if err != nil {
				t.Fatal(err)
			}
			if inventory.Filesystem != filesystem || len(inventory.Files) != 1 {
				t.Fatalf("unexpected inventory: %#v", inventory)
			}
			file := inventory.Files[0]
			if file.Path != "/hello.txt" || file.Type != "file" || file.Mode != 0o640 || file.UID != 1000 || file.GID != 1001 || file.Size != 5 || !file.ExtendedAttributes {
				t.Fatalf("unexpected file metadata: %#v", file)
			}
			if inventory.LogicalBytesRead == 0 || inventory.LogicalBytesRead > inventory.LogicalBytesLimit || inventory.FilesLimit != maxInventoryFiles {
				t.Fatalf("invalid bounds report: %#v", inventory)
			}
		})
	}
}

func TestInspectExtFilesystemRejectsCorruptDirectoryMetadata(t *testing.T) {
	path, volume := buildExtFixture(t, "ext2", true)
	if _, err := InspectExtFilesystem(path, FormatRAW, volume); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatalf("err=%v want ErrCorruptFilesystem", err)
	}
}

func TestReadExtFileIsConfinedAndDoesNotFollowNonRegularPaths(t *testing.T) {
	path, volume := buildExtFixture(t, "ext4", false)
	content, err := ReadExtFile(path, FormatRAW, volume, "/hello.txt")
	if err != nil || string(content) != "hello" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	for _, target := range []string{"hello.txt", "/../hello.txt", "/", "/missing"} {
		if _, err := ReadExtFile(path, FormatRAW, volume, target); err == nil {
			t.Fatalf("expected %q to be refused", target)
		}
	}
}

func TestExtInventoryFileLimitTruncatesDeterministically(t *testing.T) {
	path, volume := buildExtFixture(t, "ext2", false)
	ld, closer, err := openLogicalDisk(path, FormatRAW)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	r := &extReader{ld: ld, volume: volume, filesLimit: 0, bytesLimit: maxInventoryLogicalBytes, visitedDirectories: map[uint32]bool{}}
	if err := r.loadSuperblock(); err != nil {
		t.Fatal(err)
	}
	result := FilesystemInventory{Files: []InventoryFile{}}
	r.inventory = &result
	if err := r.walkDirectory(2, "/"); err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || len(result.Files) != 0 {
		t.Fatalf("unexpected limited inventory: %#v", result)
	}
}

func TestExtLogicalByteLimitFailsClosed(t *testing.T) {
	path, volume := buildExtFixture(t, "ext2", false)
	ld, closer, err := openLogicalDisk(path, FormatRAW)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	r := &extReader{ld: ld, volume: volume, filesLimit: maxInventoryFiles, bytesLimit: 1024, visitedDirectories: map[uint32]bool{}}
	if err := r.loadSuperblock(); err != nil {
		t.Fatal(err)
	}
	result := FilesystemInventory{Files: []InventoryFile{}}
	r.inventory = &result
	if err := r.walkDirectory(2, "/"); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatalf("err=%v want bounded-read failure", err)
	}
}

func buildExtFixture(t *testing.T, filesystem string, corruptDirectory bool) (string, Volume) {
	t.Helper()
	const volumeStart = 512
	const blockSize = 1024
	const blocks = 128
	volumeBytes := blocks * blockSize
	disk := make([]byte, volumeStart+volumeBytes)
	disk[446+4] = 0x83
	putLE32(disk, 446+8, 1)
	putLE32(disk, 446+12, uint32(volumeBytes/512))
	disk[510], disk[511] = 0x55, 0xaa
	volume := disk[volumeStart:]
	sb := volume[1024:2048]
	putLE32(sb, 0, 16)
	putLE32(sb, 4, blocks)
	putLE32(sb, 20, 1)
	putLE32(sb, 24, 0)
	putLE32(sb, 32, blocks)
	putLE32(sb, 40, 16)
	binary.LittleEndian.PutUint16(sb[56:58], 0xef53)
	putLE32(sb, 76, 1)
	binary.LittleEndian.PutUint16(sb[88:90], 128)
	if filesystem == "ext3" {
		putLE32(sb, 92, 0x4)
	}
	if filesystem == "ext4" {
		putLE32(sb, 96, 0x40)
	}
	group := volume[2*blockSize : 2*blockSize+32]
	putLE32(group, 8, 5)
	inodeTable := volume[5*blockSize:]
	root := inodeTable[128:256]
	binary.LittleEndian.PutUint16(root[0:2], 0x41ed)
	putLE32(root, 4, blockSize)
	binary.LittleEndian.PutUint16(root[26:28], 2)
	setInodeBlock(root, filesystem, 10)
	file := inodeTable[11*128 : 12*128]
	binary.LittleEndian.PutUint16(file[0:2], 0x81a0)
	binary.LittleEndian.PutUint16(file[2:4], 1000)
	putLE32(file, 4, 5)
	binary.LittleEndian.PutUint16(file[24:26], 1001)
	binary.LittleEndian.PutUint16(file[26:28], 1)
	putLE32(file, 104, 12)
	setInodeBlock(file, filesystem, 11)
	directory := volume[10*blockSize : 11*blockSize]
	writeExtDirEntry(directory[0:12], 2, 12, ".", 2)
	writeExtDirEntry(directory[12:24], 2, 12, "..", 2)
	lastLength := blockSize - 24
	if corruptDirectory {
		lastLength = 0
	}
	writeExtDirEntry(directory[24:], 12, lastLength, "hello.txt", 1)
	copy(volume[11*blockSize:], "hello")
	path := writeTemp(t, filesystem+".raw", disk)
	return path, Volume{Index: 0, Kind: "primary", Type: "mbr:0x83", StartBytes: volumeStart, SizeBytes: uint64(volumeBytes), Content: "unsupported-filesystem:ext2-ext3-ext4"}
}

func setInodeBlock(inode []byte, filesystem string, physical uint32) {
	if filesystem != "ext4" {
		putLE32(inode, 40, physical)
		return
	}
	putLE32(inode, 32, extentFlag)
	extent := inode[40:100]
	binary.LittleEndian.PutUint16(extent[0:2], extentMagic)
	binary.LittleEndian.PutUint16(extent[2:4], 1)
	binary.LittleEndian.PutUint16(extent[4:6], 4)
	binary.LittleEndian.PutUint16(extent[16:18], 1)
	putLE32(extent, 20, physical)
}

func writeExtDirEntry(target []byte, inode uint32, recordLength int, name string, fileType byte) {
	putLE32(target, 0, inode)
	binary.LittleEndian.PutUint16(target[4:6], uint16(recordLength))
	target[6], target[7] = byte(len(name)), fileType
	copy(target[8:], name)
}

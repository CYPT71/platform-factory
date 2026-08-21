package vmdisk

import (
	"encoding/binary"
	"errors"
	"testing"
)

func TestInspectXFSFilesystemReadsShortformAndExtentDirectories(t *testing.T) {
	for _, extentDirectory := range []bool{false, true} {
		name := "shortform"
		if extentDirectory {
			name = "extent"
		}
		t.Run(name, func(t *testing.T) {
			disk, volume := buildXFSFixture(t, extentDirectory, false)
			inventory, err := InspectXFSFilesystem(disk, FormatRAW, volume)
			if err != nil {
				t.Fatal(err)
			}
			if inventory.Filesystem != "xfs" || len(inventory.Files) != 1 || inventory.Files[0].Path != "/hello" || inventory.Files[0].Size != 5 {
				t.Fatalf("unexpected inventory: %#v", inventory)
			}
			if inventory.LogicalBytesRead == 0 || inventory.LogicalBytesRead > inventory.LogicalBytesLimit {
				t.Fatalf("invalid bounds: %#v", inventory)
			}
		})
	}
}

func TestInspectXFSFilesystemRejectsInvalidDirectoryTag(t *testing.T) {
	disk, volume := buildXFSFixture(t, true, true)
	if _, err := InspectXFSFilesystem(disk, FormatRAW, volume); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatalf("err=%v want ErrCorruptFilesystem", err)
	}
}

func buildXFSFixture(t *testing.T, extentDirectory, corruptTag bool) (string, Volume) {
	t.Helper()
	const blockSize = 4096
	image := make([]byte, 64*blockSize)
	sb := image[:512]
	copy(sb[:4], "XFSB")
	binary.BigEndian.PutUint32(sb[4:8], blockSize)
	binary.BigEndian.PutUint64(sb[8:16], 64)
	binary.BigEndian.PutUint64(sb[56:64], 32) // AG 0, block 2, inode 0.
	binary.BigEndian.PutUint32(sb[84:88], 64)
	binary.BigEndian.PutUint32(sb[88:92], 1)
	binary.BigEndian.PutUint16(sb[100:102], 4)
	binary.BigEndian.PutUint16(sb[104:106], 256)
	binary.BigEndian.PutUint16(sb[106:108], 16)
	sb[123], sb[124] = 4, 6
	binary.BigEndian.PutUint32(sb[200:204], 0x200) // v4 directory ftype feature.
	if extentDirectory {
		sb[192] = 1 // 8 KiB directory blocks exercise multi-fsblock reads.
	}

	root := image[2*blockSize : 2*blockSize+256]
	writeXFSInode(root, 0x41ed, 1, 0)
	child := image[2*blockSize+256 : 2*blockSize+512]
	writeXFSInode(child, 0x81a4, 1, 5)
	if !extentDirectory {
		data := root[100:]
		data[0], data[1] = 1, 0
		binary.BigEndian.PutUint32(data[2:6], 32)
		data[6] = 5
		binary.BigEndian.PutUint16(data[7:9], 0)
		copy(data[9:14], "hello")
		data[14] = 1
		binary.BigEndian.PutUint32(data[15:19], 33)
		binary.BigEndian.PutUint64(root[56:64], 19)
	} else {
		root[5] = 2
		binary.BigEndian.PutUint64(root[56:64], 2*blockSize)
		binary.BigEndian.PutUint32(root[76:80], 1)
		// One extent: logical blocks [0,2), physical blocks [4,6).
		binary.BigEndian.PutUint64(root[100:108], 0)
		binary.BigEndian.PutUint64(root[108:116], uint64(4)<<21|2)
		directory := image[4*blockSize : 6*blockSize]
		binary.BigEndian.PutUint32(directory[:4], 0x58443244)
		binary.BigEndian.PutUint64(directory[16:24], 33)
		directory[24] = 5
		copy(directory[25:30], "hello")
		directory[30] = 1
		tag := uint16(16)
		if corruptTag {
			tag = 17
		}
		binary.BigEndian.PutUint16(directory[31:33], tag)
		binary.BigEndian.PutUint16(directory[40:42], 0xffff)
		binary.BigEndian.PutUint16(directory[42:44], uint16(len(directory)-40))
	}
	return writeTemp(t, "xfs.raw", image), Volume{Index: 0, SizeBytes: uint64(len(image)), Content: "unsupported-filesystem:xfs"}
}

func writeXFSInode(raw []byte, mode uint16, format byte, size uint64) {
	copy(raw[:2], "IN")
	binary.BigEndian.PutUint16(raw[2:4], mode)
	raw[4], raw[5] = 2, format
	binary.BigEndian.PutUint32(raw[8:12], 1000)
	binary.BigEndian.PutUint32(raw[12:16], 1000)
	binary.BigEndian.PutUint64(raw[56:64], size)
}

package vmdisk

import (
	"encoding/binary"
	"errors"
	"testing"
	"unicode/utf16"
)

func TestInspectNTFSFilesystemReadsMFTAndPaths(t *testing.T) {
	disk, volume := buildNTFSFixture(t, false)
	inventory, err := InspectNTFSFilesystem(disk, FormatRAW, volume)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Filesystem != "ntfs" || len(inventory.Files) != 1 || inventory.Files[0].Path != "/hello.txt" || inventory.Files[0].Size != 5 {
		t.Fatalf("unexpected inventory: %#v", inventory)
	}
	if inventory.LogicalBytesRead == 0 || inventory.LogicalBytesRead > inventory.LogicalBytesLimit {
		t.Fatalf("invalid bounds: %#v", inventory)
	}
}

func TestInspectNTFSFilesystemRejectsTornRecordFixup(t *testing.T) {
	disk, volume := buildNTFSFixture(t, true)
	if _, err := InspectNTFSFilesystem(disk, FormatRAW, volume); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatalf("err=%v want ErrCorruptFilesystem", err)
	}
}

func buildNTFSFixture(t *testing.T, corruptFixup bool) (string, Volume) {
	t.Helper()
	const volumeStart = 512
	const volumeBytes = 1 << 20
	image := make([]byte, volumeStart+volumeBytes)
	image[446+4] = 0x07
	putLE32(image, 446+8, 1)
	putLE32(image, 446+12, volumeBytes/512)
	image[510], image[511] = 0x55, 0xaa
	volume := image[volumeStart:]
	boot := volume[:512]
	boot[0], boot[1], boot[2] = 0xeb, 0x52, 0x90
	copy(boot[3:11], "NTFS    ")
	binary.LittleEndian.PutUint16(boot[11:13], 512)
	boot[13] = 1
	binary.LittleEndian.PutUint64(boot[40:48], volumeBytes/512)
	binary.LittleEndian.PutUint64(boot[48:56], 4)
	boot[64] = 0xf6 // signed -10: 2^10-byte MFT records
	boot[510], boot[511] = 0x55, 0xaa
	mft := volume[4*512 : 4*512+7*1024]
	record0 := mft[:1024]
	initNTFSRecord(record0, 0, false)
	attribute := record0[56:128]
	putLE32(attribute, 0, 0x80)
	putLE32(attribute, 4, 72)
	attribute[8] = 1
	binary.LittleEndian.PutUint16(attribute[32:34], 64)
	binary.LittleEndian.PutUint64(attribute[40:48], 7*1024)
	binary.LittleEndian.PutUint64(attribute[48:56], 7*1024)
	binary.LittleEndian.PutUint64(attribute[56:64], 7*1024)
	attribute[64], attribute[65], attribute[66], attribute[67] = 0x11, 14, 4, 0
	putLE32(record0, 128, 0xffffffff)
	putLE32(record0, 24, 136)
	finishNTFSFixups(record0, false)
	root := mft[5*1024 : 6*1024]
	initNTFSRecord(root, 5, true)
	addNTFSFilename(root, 5, ".", 0)
	finishNTFSFixups(root, false)
	file := mft[6*1024 : 7*1024]
	initNTFSRecord(file, 6, false)
	addNTFSFilename(file, 5, "hello.txt", 5)
	finishNTFSFixups(file, corruptFixup)
	path := writeTemp(t, "ntfs.raw", image)
	return path, Volume{Index: 0, StartBytes: volumeStart, SizeBytes: volumeBytes, Content: "unsupported-filesystem:ntfs"}
}

func initNTFSRecord(record []byte, number uint32, directory bool) {
	copy(record[:4], "FILE")
	binary.LittleEndian.PutUint16(record[4:6], 48)
	binary.LittleEndian.PutUint16(record[6:8], 3)
	binary.LittleEndian.PutUint16(record[20:22], 56)
	flags := uint16(1)
	if directory {
		flags |= 2
	}
	binary.LittleEndian.PutUint16(record[22:24], flags)
	putLE32(record, 28, uint32(len(record)))
	putLE32(record, 44, number)
}

func addNTFSFilename(record []byte, parent uint64, name string, size uint64) {
	units := utf16.Encode([]rune(name))
	valueLength := 66 + len(units)*2
	attributeLength := (24 + valueLength + 7) &^ 7
	attribute := record[56 : 56+attributeLength]
	putLE32(attribute, 0, 0x30)
	putLE32(attribute, 4, uint32(attributeLength))
	putLE32(attribute, 16, uint32(valueLength))
	binary.LittleEndian.PutUint16(attribute[20:22], 24)
	value := attribute[24 : 24+valueLength]
	binary.LittleEndian.PutUint64(value[0:8], parent)
	binary.LittleEndian.PutUint64(value[48:56], size)
	value[64], value[65] = byte(len(units)), 1
	for index, unit := range units {
		binary.LittleEndian.PutUint16(value[66+index*2:68+index*2], unit)
	}
	putLE32(record, 56+attributeLength, 0xffffffff)
	putLE32(record, 24, uint32(56+attributeLength+8))
}

func finishNTFSFixups(record []byte, corrupt bool) {
	const sequence = 0xa55a
	binary.LittleEndian.PutUint16(record[48:50], sequence)
	binary.LittleEndian.PutUint16(record[50:52], binary.LittleEndian.Uint16(record[510:512]))
	binary.LittleEndian.PutUint16(record[52:54], binary.LittleEndian.Uint16(record[1022:1024]))
	binary.LittleEndian.PutUint16(record[510:512], sequence)
	binary.LittleEndian.PutUint16(record[1022:1024], sequence)
	if corrupt {
		record[1022] ^= 0xff
	}
}

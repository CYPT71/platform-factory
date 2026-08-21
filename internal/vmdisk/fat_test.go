package vmdisk

import (
	"encoding/binary"
	"errors"
	"testing"
	"unicode/utf16"
)

func TestInspectFATFilesystemReadsFAT12FAT16FAT32AndVFATNames(t *testing.T) {
	for _, bits := range []int{12, 16, 32} {
		t.Run("fat"+itoa(bits), func(t *testing.T) {
			disk, volume := buildFATFixture(t, bits, false)
			inventory, err := InspectFATFilesystem(disk, FormatRAW, volume)
			if err != nil {
				t.Fatal(err)
			}
			if inventory.Filesystem != "fat"+itoa(bits) || len(inventory.Files) != 1 || inventory.Files[0].Path != "/LongName.txt" || inventory.Files[0].Size != 5 {
				t.Fatalf("unexpected inventory: %#v", inventory)
			}
			if inventory.LogicalBytesRead == 0 || inventory.LogicalBytesRead > inventory.LogicalBytesLimit {
				t.Fatalf("invalid bounds: %#v", inventory)
			}
		})
	}
}

func TestInspectFATFilesystemRejectsClusterCycles(t *testing.T) {
	disk, volume := buildFATFixture(t, 16, true)
	if _, err := InspectFATFilesystem(disk, FormatRAW, volume); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatalf("err=%v want ErrCorruptFilesystem", err)
	}
}

func buildFATFixture(t *testing.T, bits int, cycle bool) (string, Volume) {
	t.Helper()
	const bytesPerSector = 512
	clusters := map[int]int{12: 100, 16: 5000, 32: 65530}[bits]
	reserved := 1
	rootEntries := 16
	if bits == 32 {
		rootEntries = 0
	}
	rootSectors := (rootEntries*32 + bytesPerSector - 1) / bytesPerSector
	fatBytes := ((clusters+2)*bits + 7) / 8
	fatSectors := (fatBytes + bytesPerSector - 1) / bytesPerSector
	totalSectors := reserved + fatSectors + rootSectors + clusters
	volumeBytes := totalSectors * bytesPerSector
	const volumeStart = 512
	image := make([]byte, volumeStart+volumeBytes)
	image[446+4] = 0x0c
	putLE32(image, 446+8, 1)
	putLE32(image, 446+12, uint32(volumeBytes/512))
	image[510], image[511] = 0x55, 0xaa
	volumeData := image[volumeStart:]
	boot := volumeData[:512]
	boot[0], boot[1], boot[2] = 0xeb, 0x3c, 0x90
	binary.LittleEndian.PutUint16(boot[11:13], bytesPerSector)
	boot[13] = 1
	binary.LittleEndian.PutUint16(boot[14:16], uint16(reserved))
	boot[16] = 1
	binary.LittleEndian.PutUint16(boot[17:19], uint16(rootEntries))
	if totalSectors <= 0xffff {
		binary.LittleEndian.PutUint16(boot[19:21], uint16(totalSectors))
	} else {
		putLE32(boot, 32, uint32(totalSectors))
	}
	if bits == 32 {
		putLE32(boot, 36, uint32(fatSectors))
		putLE32(boot, 44, 2)
		copy(boot[82:90], "FAT32   ")
	} else {
		binary.LittleEndian.PutUint16(boot[22:24], uint16(fatSectors))
		copy(boot[54:62], map[int]string{12: "FAT12   ", 16: "FAT16   "}[bits])
	}
	boot[510], boot[511] = 0x55, 0xaa
	fat := volumeData[reserved*bytesPerSector : (reserved+fatSectors)*bytesPerSector]
	setFATEntry(fat, bits, 0, map[int]uint32{12: 0xff8, 16: 0xfff8, 32: 0x0ffffff8}[bits])
	setFATEntry(fat, bits, 1, map[int]uint32{12: 0xfff, 16: 0xffff, 32: 0x0fffffff}[bits])
	firstDataSector := reserved + fatSectors + rootSectors
	var directory []byte
	fileCluster := uint32(2)
	if bits == 32 {
		directory = volumeData[firstDataSector*bytesPerSector : (firstDataSector+1)*bytesPerSector]
		setFATEntry(fat, bits, 2, 0x0fffffff)
		fileCluster = 3
	} else {
		rootStart := (reserved + fatSectors) * bytesPerSector
		directory = volumeData[rootStart : rootStart+rootSectors*bytesPerSector]
	}
	if cycle {
		writeFATShortEntry(directory[:32], "SUBDIR     ", 0x10, 2, 0)
		setFATEntry(fat, bits, 2, 2)
		return writeTemp(t, "fat-cycle.raw", image), Volume{Index: 0, StartBytes: volumeStart, SizeBytes: uint64(volumeBytes), Content: "unsupported-filesystem:fat"}
	}
	shortName := []byte("LONGNA~1TXT")
	writeVFATEntry(directory[:32], "LongName.txt", fatShortNameChecksum(shortName))
	writeFATShortEntry(directory[32:64], string(shortName), 0x20, fileCluster, 5)
	setFATEntry(fat, bits, fileCluster, map[int]uint32{12: 0xfff, 16: 0xffff, 32: 0x0fffffff}[bits])
	fileSector := firstDataSector + int(fileCluster-2)
	copy(volumeData[fileSector*bytesPerSector:], "hello")
	return writeTemp(t, "fat.raw", image), Volume{Index: 0, StartBytes: volumeStart, SizeBytes: uint64(volumeBytes), Content: "unsupported-filesystem:fat"}
}

func setFATEntry(fat []byte, bits int, cluster, value uint32) {
	switch bits {
	case 12:
		offset := int(cluster + cluster/2)
		current := binary.LittleEndian.Uint16(fat[offset : offset+2])
		if cluster&1 == 0 {
			current = current&0xf000 | uint16(value&0x0fff)
		} else {
			current = current&0x000f | uint16(value&0x0fff)<<4
		}
		binary.LittleEndian.PutUint16(fat[offset:offset+2], current)
	case 16:
		binary.LittleEndian.PutUint16(fat[cluster*2:cluster*2+2], uint16(value))
	case 32:
		binary.LittleEndian.PutUint32(fat[cluster*4:cluster*4+4], value)
	}
}

func writeFATShortEntry(entry []byte, name string, attributes byte, cluster uint32, size uint32) {
	copy(entry[:11], name)
	entry[11] = attributes
	binary.LittleEndian.PutUint16(entry[20:22], uint16(cluster>>16))
	binary.LittleEndian.PutUint16(entry[26:28], uint16(cluster))
	putLE32(entry, 28, size)
}

func writeVFATEntry(entry []byte, name string, checksum byte) {
	for i := range entry {
		entry[i] = 0xff
	}
	entry[0], entry[11], entry[12], entry[13], entry[26], entry[27] = 0x41, 0x0f, 0, checksum, 0, 0
	units := append(utf16.Encode([]rune(name)), 0)
	for len(units) < 13 {
		units = append(units, 0xffff)
	}
	positions := []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30}
	for i, position := range positions {
		binary.LittleEndian.PutUint16(entry[position:position+2], units[i])
	}
}

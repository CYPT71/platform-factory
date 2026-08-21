package vmdisk

import (
	"encoding/binary"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf16"
)

type fatReader struct {
	ld                logicalDisk
	volume            Volume
	bytesPerSector    uint64
	sectorsPerCluster uint64
	reservedSectors   uint64
	fatStart          uint64
	fatSectors        uint64
	rootEntryCount    uint32
	rootDirStart      uint64
	rootDirSectors    uint64
	firstDataSector   uint64
	clusterCount      uint32
	rootCluster       uint32
	fatBits           int
	bytesRead         uint64
	bytesLimit        uint64
	filesLimit        int
	visitedDirs       map[uint32]bool
	inventory         *FilesystemInventory
}

// InspectFATFilesystem inventories FAT12, FAT16 and FAT32 without mounting or
// executing anything from the image. VFAT long names are checksum-validated.
func InspectFATFilesystem(diskPath string, format Format, volume Volume) (FilesystemInventory, error) {
	ld, closer, err := openLogicalDisk(diskPath, format)
	if err != nil {
		return FilesystemInventory{}, err
	}
	defer closer.Close()
	r := &fatReader{ld: ld, volume: volume, bytesLimit: maxInventoryLogicalBytes, filesLimit: maxInventoryFiles, visitedDirs: map[uint32]bool{}}
	if err := r.loadBootSector(); err != nil {
		return FilesystemInventory{}, err
	}
	result := FilesystemInventory{VolumeIndex: volume.Index, Filesystem: fmt.Sprintf("fat%d", r.fatBits), Files: []InventoryFile{}, FilesLimit: r.filesLimit, LogicalBytesLimit: r.bytesLimit, UnsupportedFeatures: []string{}}
	r.inventory = &result
	if r.fatBits == 32 {
		if err := r.walkClusterDirectory(r.rootCluster, "/"); err != nil {
			return FilesystemInventory{}, err
		}
	} else {
		data, err := r.read(r.rootDirStart, r.rootDirSectors*r.bytesPerSector)
		if err != nil {
			return FilesystemInventory{}, err
		}
		if err := r.walkDirectoryData(data, "/"); err != nil {
			return FilesystemInventory{}, err
		}
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	result.LogicalBytesRead = r.bytesRead
	return result, nil
}

func (r *fatReader) read(relative, length uint64) ([]byte, error) {
	if relative > r.volume.SizeBytes || length > r.volume.SizeBytes-relative || length > maxMappedRegionSize || length > r.bytesLimit || r.bytesRead > r.bytesLimit-length {
		return nil, fmt.Errorf("%w: FAT read is outside volume or analysis budget", ErrCorruptFilesystem)
	}
	absolute := r.volume.StartBytes + relative
	if absolute < r.volume.StartBytes || absolute > uint64(^uint64(0)>>1) {
		return nil, fmt.Errorf("%w: FAT offset overflows", ErrCorruptFilesystem)
	}
	data, err := r.ld.ReadLogical(int64(absolute), int64(length))
	if err != nil {
		return nil, err
	}
	r.bytesRead += length
	return data, nil
}

func (r *fatReader) loadBootSector() error {
	boot, err := r.read(0, 512)
	if err != nil {
		return err
	}
	if boot[510] != 0x55 || boot[511] != 0xaa {
		return fmt.Errorf("%w: FAT boot signature missing", ErrCorruptFilesystem)
	}
	r.bytesPerSector = uint64(binary.LittleEndian.Uint16(boot[11:13]))
	r.sectorsPerCluster = uint64(boot[13])
	r.reservedSectors = uint64(binary.LittleEndian.Uint16(boot[14:16]))
	fats := uint64(boot[16])
	r.rootEntryCount = uint32(binary.LittleEndian.Uint16(boot[17:19]))
	totalSectors := uint64(binary.LittleEndian.Uint16(boot[19:21]))
	if totalSectors == 0 {
		totalSectors = uint64(binary.LittleEndian.Uint32(boot[32:36]))
	}
	r.fatSectors = uint64(binary.LittleEndian.Uint16(boot[22:24]))
	if r.fatSectors == 0 {
		r.fatSectors = uint64(binary.LittleEndian.Uint32(boot[36:40]))
	}
	if r.bytesPerSector < 512 || r.bytesPerSector > 4096 || r.bytesPerSector&(r.bytesPerSector-1) != 0 || r.sectorsPerCluster == 0 || r.sectorsPerCluster > 128 || r.sectorsPerCluster&(r.sectorsPerCluster-1) != 0 || r.reservedSectors == 0 || fats == 0 || fats > 2 || totalSectors == 0 || r.fatSectors == 0 {
		return fmt.Errorf("%w: inconsistent FAT BIOS parameter block", ErrCorruptFilesystem)
	}
	r.rootDirSectors = (uint64(r.rootEntryCount)*32 + r.bytesPerSector - 1) / r.bytesPerSector
	r.fatStart = r.reservedSectors * r.bytesPerSector
	r.rootDirStart = (r.reservedSectors + fats*r.fatSectors) * r.bytesPerSector
	r.firstDataSector = r.reservedSectors + fats*r.fatSectors + r.rootDirSectors
	if totalSectors <= r.firstDataSector || totalSectors > r.volume.SizeBytes/r.bytesPerSector {
		return fmt.Errorf("%w: FAT geometry exceeds volume", ErrCorruptFilesystem)
	}
	clusters := (totalSectors - r.firstDataSector) / r.sectorsPerCluster
	if clusters == 0 || clusters > uint64(^uint32(0)-2) {
		return fmt.Errorf("%w: FAT cluster count is invalid", ErrCorruptFilesystem)
	}
	r.clusterCount = uint32(clusters)
	switch {
	case clusters < 4085:
		r.fatBits = 12
	case clusters < 65525:
		r.fatBits = 16
	default:
		r.fatBits = 32
		r.rootCluster = binary.LittleEndian.Uint32(boot[44:48]) & 0x0fffffff
		if r.rootEntryCount != 0 || r.rootCluster < 2 || r.rootCluster >= r.clusterCount+2 {
			return fmt.Errorf("%w: FAT32 root geometry is invalid", ErrCorruptFilesystem)
		}
	}
	minimumFATBytes := (uint64(r.clusterCount+2)*uint64(r.fatBits) + 7) / 8
	if r.fatSectors*r.bytesPerSector < minimumFATBytes {
		return fmt.Errorf("%w: FAT table is too short for cluster count", ErrCorruptFilesystem)
	}
	return nil
}

func (r *fatReader) clusterData(cluster uint32) ([]byte, error) {
	if cluster < 2 || cluster >= r.clusterCount+2 {
		return nil, fmt.Errorf("%w: FAT cluster %d is outside data area", ErrCorruptFilesystem, cluster)
	}
	sector := r.firstDataSector + uint64(cluster-2)*r.sectorsPerCluster
	return r.read(sector*r.bytesPerSector, r.sectorsPerCluster*r.bytesPerSector)
}

func (r *fatReader) nextCluster(cluster uint32) (uint32, bool, error) {
	var offset, length uint64
	switch r.fatBits {
	case 12:
		offset, length = uint64(cluster)+uint64(cluster)/2, 2
	case 16:
		offset, length = uint64(cluster)*2, 2
	case 32:
		offset, length = uint64(cluster)*4, 4
	}
	raw, err := r.read(r.fatStart+offset, length)
	if err != nil {
		return 0, false, err
	}
	var value uint32
	switch r.fatBits {
	case 12:
		value = uint32(binary.LittleEndian.Uint16(raw))
		if cluster&1 == 0 {
			value &= 0x0fff
		} else {
			value >>= 4
		}
	case 16:
		value = uint32(binary.LittleEndian.Uint16(raw))
	case 32:
		value = binary.LittleEndian.Uint32(raw) & 0x0fffffff
	}
	eoc := map[int]uint32{12: 0x0ff8, 16: 0xfff8, 32: 0x0ffffff8}[r.fatBits]
	bad := map[int]uint32{12: 0x0ff7, 16: 0xfff7, 32: 0x0ffffff7}[r.fatBits]
	if value >= eoc {
		return 0, true, nil
	}
	if value == bad || value < 2 || value >= r.clusterCount+2 {
		return 0, false, fmt.Errorf("%w: FAT chain references reserved/bad cluster %#x", ErrCorruptFilesystem, value)
	}
	return value, false, nil
}

func (r *fatReader) clusterChain(start uint32) ([]byte, error) {
	visited := map[uint32]bool{}
	var result []byte
	for cluster := start; ; {
		if visited[cluster] {
			return nil, fmt.Errorf("%w: FAT cluster chain cycle at %d", ErrCorruptFilesystem, cluster)
		}
		visited[cluster] = true
		data, err := r.clusterData(cluster)
		if err != nil {
			return nil, err
		}
		result = append(result, data...)
		next, done, err := r.nextCluster(cluster)
		if err != nil {
			return nil, err
		}
		if done {
			return result, nil
		}
		cluster = next
	}
}

func (r *fatReader) walkClusterDirectory(cluster uint32, directoryPath string) error {
	if r.visitedDirs[cluster] {
		return fmt.Errorf("%w: FAT directory cluster cycle at %d", ErrCorruptFilesystem, cluster)
	}
	r.visitedDirs[cluster] = true
	data, err := r.clusterChain(cluster)
	if err != nil {
		return err
	}
	return r.walkDirectoryData(data, directoryPath)
}

func (r *fatReader) walkDirectoryData(data []byte, directoryPath string) error {
	var longEntries [][]byte
	for offset := 0; offset+32 <= len(data); offset += 32 {
		entry := data[offset : offset+32]
		if entry[0] == 0x00 {
			return nil
		}
		if entry[0] == 0xe5 {
			longEntries = nil
			continue
		}
		attributes := entry[11]
		if attributes == 0x0f {
			longEntries = append(longEntries, append([]byte(nil), entry...))
			continue
		}
		if attributes&0x08 != 0 { // volume label
			longEntries = nil
			continue
		}
		name, err := fatEntryName(entry, longEntries)
		longEntries = nil
		if err != nil {
			return err
		}
		if name == "." || name == ".." {
			continue
		}
		if name == "" || strings.ContainsAny(name, "/\\\x00") {
			return fmt.Errorf("%w: unsafe FAT filename", ErrCorruptFilesystem)
		}
		cluster := uint32(binary.LittleEndian.Uint16(entry[26:28])) | uint32(binary.LittleEndian.Uint16(entry[20:22]))<<16
		size := uint64(binary.LittleEndian.Uint32(entry[28:32]))
		isDirectory := attributes&0x10 != 0
		if isDirectory && cluster < 2 {
			return fmt.Errorf("%w: FAT directory has invalid start cluster", ErrCorruptFilesystem)
		}
		if !isDirectory && size > 0 && (cluster < 2 || cluster >= r.clusterCount+2) {
			return fmt.Errorf("%w: FAT file has invalid start cluster", ErrCorruptFilesystem)
		}
		if len(r.inventory.Files) >= r.filesLimit {
			r.inventory.Truncated = true
			return nil
		}
		kind, mode := "file", uint16(0o444)
		if isDirectory {
			kind, mode, size = "directory", 0o555, 0
		}
		pathname := path.Join(directoryPath, name)
		r.inventory.Files = append(r.inventory.Files, InventoryFile{Path: pathname, Type: kind, Mode: mode, Size: size})
		if isDirectory {
			if err := r.walkClusterDirectory(cluster, pathname); err != nil {
				return err
			}
		}
	}
	if len(data)%32 != 0 {
		return fmt.Errorf("%w: truncated FAT directory", ErrCorruptFilesystem)
	}
	return nil
}

func fatEntryName(short []byte, longEntries [][]byte) (string, error) {
	if len(longEntries) == 0 {
		base := strings.TrimSpace(string(short[0:8]))
		ext := strings.TrimSpace(string(short[8:11]))
		if short[0] == 0x05 {
			base = string(append([]byte{0xe5}, short[1:8]...))
		}
		if ext != "" {
			return base + "." + ext, nil
		}
		return base, nil
	}
	checksum := fatShortNameChecksum(short[:11])
	count := len(longEntries)
	units := make([]uint16, count*13)
	seen := make([]bool, count)
	for _, entry := range longEntries {
		sequence := int(entry[0] & 0x1f)
		if sequence < 1 || sequence > count || seen[sequence-1] || entry[13] != checksum {
			return "", fmt.Errorf("%w: invalid VFAT long-name sequence", ErrCorruptFilesystem)
		}
		seen[sequence-1] = true
		positions := [][2]int{{1, 11}, {14, 26}, {28, 32}}
		index := (sequence - 1) * 13
		for _, position := range positions {
			for offset := position[0]; offset < position[1]; offset += 2 {
				units[index] = binary.LittleEndian.Uint16(entry[offset : offset+2])
				index++
			}
		}
	}
	var terminated bool
	clean := units[:0]
	for _, unit := range units {
		if unit == 0 || unit == 0xffff {
			terminated = true
			continue
		}
		if terminated {
			return "", fmt.Errorf("%w: invalid VFAT name padding", ErrCorruptFilesystem)
		}
		clean = append(clean, unit)
	}
	return string(utf16.Decode(clean)), nil
}

func fatShortNameChecksum(name []byte) byte {
	var sum byte
	for _, value := range name {
		sum = ((sum & 1) << 7) + (sum >> 1) + value
	}
	return sum
}

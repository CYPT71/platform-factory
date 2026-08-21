package vmdisk

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"
)

type ntfsRun struct {
	virtualStart uint64
	length       uint64
	physical     uint64
}

type ntfsRecord struct {
	number uint64
	parent uint64
	name   string
	kind   string
	size   uint64
}

type ntfsReader struct {
	ld             logicalDisk
	volume         Volume
	bytesPerSector uint64
	clusterSize    uint64
	recordSize     uint64
	mftCluster     uint64
	totalClusters  uint64
	bytesRead      uint64
	bytesLimit     uint64
	filesLimit     int
}

// InspectNTFSFilesystem inventories an NTFS volume through its $MFT runlist.
// It applies update-sequence fixups before trusting any FILE record and never
// follows reparse points or mounts the volume.
func InspectNTFSFilesystem(diskPath string, format Format, volume Volume) (FilesystemInventory, error) {
	ld, closer, err := openLogicalDisk(diskPath, format)
	if err != nil {
		return FilesystemInventory{}, err
	}
	defer closer.Close()
	r := &ntfsReader{ld: ld, volume: volume, bytesLimit: maxInventoryLogicalBytes, filesLimit: maxInventoryFiles}
	if err := r.loadBootSector(); err != nil {
		return FilesystemInventory{}, err
	}
	first, err := r.read(r.mftCluster*r.clusterSize, r.recordSize)
	if err != nil {
		return FilesystemInventory{}, err
	}
	if err := r.applyFixups(first); err != nil {
		return FilesystemInventory{}, err
	}
	runs, mftSize, err := r.mftRuns(first)
	if err != nil {
		return FilesystemInventory{}, err
	}
	if mftSize%r.recordSize != 0 {
		return FilesystemInventory{}, fmt.Errorf("%w: NTFS $MFT size is not record-aligned", ErrCorruptFilesystem)
	}
	recordCount := mftSize / r.recordSize
	if recordCount == 0 {
		return FilesystemInventory{}, fmt.Errorf("%w: NTFS $MFT contains no records", ErrCorruptFilesystem)
	}
	truncated := false
	if recordCount > uint64(r.filesLimit+64) {
		recordCount = uint64(r.filesLimit + 64)
		truncated = true
	}
	records := map[uint64]ntfsRecord{}
	for index := uint64(0); index < recordCount; index++ {
		raw, err := r.readMFT(runs, index*r.recordSize, r.recordSize)
		if err != nil {
			return FilesystemInventory{}, err
		}
		if string(raw[:4]) != "FILE" {
			continue // unused tail records may be zero
		}
		if err := r.applyFixups(raw); err != nil {
			return FilesystemInventory{}, fmt.Errorf("NTFS MFT record %d: %w", index, err)
		}
		record, inUse, err := r.parseRecord(raw, index)
		if err != nil {
			return FilesystemInventory{}, err
		}
		if inUse && record.name != "" {
			records[record.number] = record
		}
	}
	result := FilesystemInventory{VolumeIndex: volume.Index, Filesystem: "ntfs", Files: []InventoryFile{}, FilesLimit: r.filesLimit, LogicalBytesLimit: r.bytesLimit, Truncated: truncated, UnsupportedFeatures: []string{"reparse points are inventoried but never followed", "compressed/encrypted file content is not read"}}
	for number, record := range records {
		if number == 5 { // NTFS root directory
			continue
		}
		pathname, err := ntfsRecordPath(number, records)
		if err != nil {
			return FilesystemInventory{}, err
		}
		if pathname == "" {
			continue // metadata not reachable from root
		}
		if len(result.Files) >= r.filesLimit {
			result.Truncated = true
			break
		}
		mode := uint16(0o444)
		if record.kind == "directory" {
			mode = 0o555
		}
		result.Files = append(result.Files, InventoryFile{Path: pathname, Type: record.kind, Mode: mode, Size: record.size})
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	result.LogicalBytesRead = r.bytesRead
	return result, nil
}

func (r *ntfsReader) read(relative, length uint64) ([]byte, error) {
	if relative > r.volume.SizeBytes || length > r.volume.SizeBytes-relative || length > maxMappedRegionSize || length > r.bytesLimit || r.bytesRead > r.bytesLimit-length {
		return nil, fmt.Errorf("%w: NTFS read is outside volume or analysis budget", ErrCorruptFilesystem)
	}
	absolute := r.volume.StartBytes + relative
	if absolute < r.volume.StartBytes || absolute > uint64(^uint64(0)>>1) {
		return nil, fmt.Errorf("%w: NTFS offset overflows", ErrCorruptFilesystem)
	}
	data, err := r.ld.ReadLogical(int64(absolute), int64(length))
	if err != nil {
		return nil, err
	}
	r.bytesRead += length
	return data, nil
}

func (r *ntfsReader) loadBootSector() error {
	boot, err := r.read(0, 512)
	if err != nil {
		return err
	}
	if string(boot[3:11]) != "NTFS    " || boot[510] != 0x55 || boot[511] != 0xaa {
		return fmt.Errorf("%w: NTFS boot signature missing", ErrCorruptFilesystem)
	}
	r.bytesPerSector = uint64(binary.LittleEndian.Uint16(boot[11:13]))
	sectorsPerCluster := uint64(boot[13])
	totalSectors := binary.LittleEndian.Uint64(boot[40:48])
	r.mftCluster = binary.LittleEndian.Uint64(boot[48:56])
	if r.bytesPerSector < 512 || r.bytesPerSector > 4096 || r.bytesPerSector&(r.bytesPerSector-1) != 0 || sectorsPerCluster == 0 || sectorsPerCluster > 128 || sectorsPerCluster&(sectorsPerCluster-1) != 0 || totalSectors == 0 {
		return fmt.Errorf("%w: inconsistent NTFS boot geometry", ErrCorruptFilesystem)
	}
	r.clusterSize = r.bytesPerSector * sectorsPerCluster
	r.totalClusters = totalSectors / sectorsPerCluster
	if totalSectors > r.volume.SizeBytes/r.bytesPerSector || r.mftCluster >= r.totalClusters {
		return fmt.Errorf("%w: NTFS geometry exceeds volume", ErrCorruptFilesystem)
	}
	recordCode := int8(boot[64])
	if recordCode < 0 {
		shift := uint8(-recordCode)
		if shift > 20 {
			return fmt.Errorf("%w: NTFS record-size exponent is invalid", ErrCorruptFilesystem)
		}
		r.recordSize = uint64(1) << shift
	} else {
		r.recordSize = uint64(recordCode) * r.clusterSize
	}
	if r.recordSize < r.bytesPerSector || r.recordSize > 64*1024 || r.recordSize%r.bytesPerSector != 0 {
		return fmt.Errorf("%w: NTFS MFT record size is invalid", ErrCorruptFilesystem)
	}
	return nil
}

func (r *ntfsReader) applyFixups(record []byte) error {
	if len(record) < 8 {
		return fmt.Errorf("%w: truncated NTFS record", ErrCorruptFilesystem)
	}
	offset, count := int(binary.LittleEndian.Uint16(record[4:6])), int(binary.LittleEndian.Uint16(record[6:8]))
	sectors := len(record) / int(r.bytesPerSector)
	if count != sectors+1 || offset < 8 || offset+count*2 > len(record) {
		return fmt.Errorf("%w: invalid NTFS update sequence array", ErrCorruptFilesystem)
	}
	sequence := binary.LittleEndian.Uint16(record[offset : offset+2])
	for sector := 0; sector < sectors; sector++ {
		end := (sector+1)*int(r.bytesPerSector) - 2
		if binary.LittleEndian.Uint16(record[end:end+2]) != sequence {
			return fmt.Errorf("%w: NTFS torn-write fixup mismatch", ErrCorruptFilesystem)
		}
		copy(record[end:end+2], record[offset+2+sector*2:offset+4+sector*2])
	}
	return nil
}

func (r *ntfsReader) mftRuns(record []byte) ([]ntfsRun, uint64, error) {
	attributes, err := ntfsAttributes(record)
	if err != nil {
		return nil, 0, err
	}
	for _, attribute := range attributes {
		if binary.LittleEndian.Uint32(attribute[0:4]) != 0x80 || attribute[8] == 0 {
			continue
		}
		runOffset := int(binary.LittleEndian.Uint16(attribute[32:34]))
		if len(attribute) < 56 || runOffset < 0 || runOffset >= len(attribute) {
			return nil, 0, fmt.Errorf("%w: invalid NTFS $MFT data attribute", ErrCorruptFilesystem)
		}
		runs, err := parseNTFSRunlist(attribute[runOffset:], r.clusterSize, r.totalClusters)
		return runs, binary.LittleEndian.Uint64(attribute[48:56]), err
	}
	return nil, 0, fmt.Errorf("%w: NTFS $MFT has no non-resident data runlist", ErrCorruptFilesystem)
}

func parseNTFSRunlist(raw []byte, clusterSize, totalClusters uint64) ([]ntfsRun, error) {
	var runs []ntfsRun
	var virtual, previousLCN uint64
	for offset := 0; offset < len(raw); {
		header := raw[offset]
		offset++
		if header == 0 {
			if len(runs) == 0 {
				return nil, fmt.Errorf("%w: empty NTFS runlist", ErrCorruptFilesystem)
			}
			return runs, nil
		}
		lengthBytes, offsetBytes := int(header&0x0f), int(header>>4)
		if lengthBytes < 1 || lengthBytes > 8 || offsetBytes < 1 || offsetBytes > 8 || offset+lengthBytes+offsetBytes > len(raw) {
			return nil, fmt.Errorf("%w: malformed NTFS runlist", ErrCorruptFilesystem)
		}
		length := decodeUnsignedLE(raw[offset : offset+lengthBytes])
		offset += lengthBytes
		delta := decodeSignedLE(raw[offset : offset+offsetBytes])
		offset += offsetBytes
		if length == 0 || delta == -1<<63 || delta < 0 && uint64(-delta) > previousLCN {
			return nil, fmt.Errorf("%w: invalid NTFS run geometry", ErrCorruptFilesystem)
		}
		lcn := previousLCN
		if delta < 0 {
			lcn -= uint64(-delta)
		} else {
			lcn += uint64(delta)
		}
		if lcn >= totalClusters || length > totalClusters-lcn || length > ^uint64(0)/clusterSize || virtual > ^uint64(0)-length*clusterSize {
			return nil, fmt.Errorf("%w: NTFS run escapes volume", ErrCorruptFilesystem)
		}
		runs = append(runs, ntfsRun{virtualStart: virtual, length: length * clusterSize, physical: lcn * clusterSize})
		virtual += length * clusterSize
		previousLCN = lcn
	}
	return nil, fmt.Errorf("%w: unterminated NTFS runlist", ErrCorruptFilesystem)
}

func decodeUnsignedLE(raw []byte) uint64 {
	var value uint64
	for i := len(raw) - 1; i >= 0; i-- {
		value = value<<8 | uint64(raw[i])
	}
	return value
}

func decodeSignedLE(raw []byte) int64 {
	value := decodeUnsignedLE(raw)
	if raw[len(raw)-1]&0x80 != 0 && len(raw) < 8 {
		value |= ^uint64(0) << (uint(len(raw)) * 8)
	}
	return int64(value)
}

func (r *ntfsReader) readMFT(runs []ntfsRun, offset, length uint64) ([]byte, error) {
	result := make([]byte, 0, int(length))
	remaining := length
	for remaining > 0 {
		found := false
		for _, run := range runs {
			if offset < run.virtualStart || offset >= run.virtualStart+run.length {
				continue
			}
			within := offset - run.virtualStart
			chunk := run.length - within
			if chunk > remaining {
				chunk = remaining
			}
			data, err := r.read(run.physical+within, chunk)
			if err != nil {
				return nil, err
			}
			result = append(result, data...)
			offset, remaining, found = offset+chunk, remaining-chunk, true
			break
		}
		if !found {
			return nil, fmt.Errorf("%w: NTFS $MFT runlist has a hole", ErrCorruptFilesystem)
		}
	}
	return result, nil
}

func ntfsAttributes(record []byte) ([][]byte, error) {
	if len(record) < 28 || string(record[:4]) != "FILE" {
		return nil, fmt.Errorf("%w: invalid NTFS FILE record", ErrCorruptFilesystem)
	}
	offset := int(binary.LittleEndian.Uint16(record[20:22]))
	used := int(binary.LittleEndian.Uint32(record[24:28]))
	if offset < 24 || used > len(record) || offset >= used {
		return nil, fmt.Errorf("%w: invalid NTFS attribute bounds", ErrCorruptFilesystem)
	}
	var result [][]byte
	for offset+4 <= used {
		typeCode := binary.LittleEndian.Uint32(record[offset : offset+4])
		if typeCode == 0xffffffff {
			return result, nil
		}
		if offset+16 > used {
			return nil, fmt.Errorf("%w: truncated NTFS attribute", ErrCorruptFilesystem)
		}
		length := int(binary.LittleEndian.Uint32(record[offset+4 : offset+8]))
		if length < 16 || offset+length > used || length%8 != 0 {
			return nil, fmt.Errorf("%w: invalid NTFS attribute length", ErrCorruptFilesystem)
		}
		result = append(result, record[offset:offset+length])
		offset += length
	}
	return nil, fmt.Errorf("%w: NTFS attribute list has no terminator", ErrCorruptFilesystem)
}

func (r *ntfsReader) parseRecord(raw []byte, fallback uint64) (ntfsRecord, bool, error) {
	if binary.LittleEndian.Uint16(raw[22:24])&1 == 0 {
		return ntfsRecord{}, false, nil
	}
	number := fallback
	if len(raw) >= 48 {
		number = uint64(binary.LittleEndian.Uint32(raw[44:48]))
	}
	attributes, err := ntfsAttributes(raw)
	if err != nil {
		return ntfsRecord{}, false, err
	}
	record := ntfsRecord{number: number, kind: "file"}
	if binary.LittleEndian.Uint16(raw[22:24])&2 != 0 {
		record.kind = "directory"
	}
	for _, attribute := range attributes {
		typeCode := binary.LittleEndian.Uint32(attribute[0:4])
		if typeCode == 0x30 && attribute[8] == 0 {
			valueLength := int(binary.LittleEndian.Uint32(attribute[16:20]))
			valueOffset := int(binary.LittleEndian.Uint16(attribute[20:22]))
			if valueOffset < 24 || valueLength < 66 || valueOffset+valueLength > len(attribute) {
				return ntfsRecord{}, false, fmt.Errorf("%w: invalid NTFS FILE_NAME attribute", ErrCorruptFilesystem)
			}
			value := attribute[valueOffset : valueOffset+valueLength]
			nameLength := int(value[64])
			if 66+nameLength*2 > len(value) {
				return ntfsRecord{}, false, fmt.Errorf("%w: truncated NTFS filename", ErrCorruptFilesystem)
			}
			units := make([]uint16, nameLength)
			for i := range units {
				units[i] = binary.LittleEndian.Uint16(value[66+i*2 : 68+i*2])
			}
			name := string(utf16.Decode(units))
			if name == "" || strings.ContainsAny(name, "/\\\x00") {
				return ntfsRecord{}, false, fmt.Errorf("%w: unsafe NTFS filename", ErrCorruptFilesystem)
			}
			// Prefer Win32/Win32+DOS/POSIX namespaces over a DOS-only alias.
			if record.name == "" || value[65] != 2 {
				record.parent = binary.LittleEndian.Uint64(value[0:8]) & 0x0000ffffffffffff
				record.name = name
				record.size = binary.LittleEndian.Uint64(value[48:56])
			}
		}
	}
	return record, true, nil
}

func ntfsRecordPath(number uint64, records map[uint64]ntfsRecord) (string, error) {
	parts := []string{}
	visited := map[uint64]bool{}
	for number != 5 {
		if visited[number] {
			return "", fmt.Errorf("%w: NTFS parent cycle at record %d", ErrCorruptFilesystem, number)
		}
		visited[number] = true
		record, ok := records[number]
		if !ok || record.name == "" {
			return "", nil
		}
		parts = append(parts, record.name)
		number = record.parent
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return "/" + strings.Join(parts, "/"), nil
}

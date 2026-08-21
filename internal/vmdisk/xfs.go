package vmdisk

import (
	"encoding/binary"
	"fmt"
	"path"
	"sort"
	"strings"
)

type xfsReader struct {
	ld               logicalDisk
	volume           Volume
	blockSize        uint64
	dblocks          uint64
	rootInode        uint64
	agBlocks         uint64
	agCount          uint32
	inodeSize        uint64
	inodesPerBlock   uint64
	inodePerBlockLog uint8
	agBlockLog       uint8
	dirBlockSize     uint64
	hasFileType      bool
	bytesRead        uint64
	bytesLimit       uint64
	filesLimit       int
	visitedDirs      map[uint64]bool
	inventory        *FilesystemInventory
}

type xfsInode struct {
	number   uint64
	mode     uint16
	uid      uint32
	gid      uint32
	size     uint64
	format   byte
	data     []byte
	nextents uint32
}

func InspectXFSFilesystem(diskPath string, format Format, volume Volume) (FilesystemInventory, error) {
	ld, closer, err := openLogicalDisk(diskPath, format)
	if err != nil {
		return FilesystemInventory{}, err
	}
	defer closer.Close()
	r := &xfsReader{ld: ld, volume: volume, bytesLimit: maxInventoryLogicalBytes, filesLimit: maxInventoryFiles, visitedDirs: map[uint64]bool{}}
	if err := r.loadSuperblock(); err != nil {
		return FilesystemInventory{}, err
	}
	result := FilesystemInventory{VolumeIndex: volume.Index, Filesystem: "xfs", Files: []InventoryFile{}, FilesLimit: r.filesLimit, LogicalBytesLimit: r.bytesLimit, UnsupportedFeatures: []string{"directory and file btree forks are rejected rather than guessed"}}
	r.inventory = &result
	if err := r.walkDirectory(r.rootInode, "/"); err != nil {
		return FilesystemInventory{}, err
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	result.LogicalBytesRead = r.bytesRead
	return result, nil
}

func (r *xfsReader) read(relative, length uint64) ([]byte, error) {
	if relative > r.volume.SizeBytes || length > r.volume.SizeBytes-relative || length > maxMappedRegionSize || length > r.bytesLimit || r.bytesRead > r.bytesLimit-length {
		return nil, fmt.Errorf("%w: XFS read is outside volume or analysis budget", ErrCorruptFilesystem)
	}
	absolute := r.volume.StartBytes + relative
	if absolute < r.volume.StartBytes || absolute > uint64(^uint64(0)>>1) {
		return nil, fmt.Errorf("%w: XFS offset overflows", ErrCorruptFilesystem)
	}
	data, err := r.ld.ReadLogical(int64(absolute), int64(length))
	if err != nil {
		return nil, err
	}
	r.bytesRead += length
	return data, nil
}

func (r *xfsReader) loadSuperblock() error {
	sb, err := r.read(0, 512)
	if err != nil {
		return err
	}
	if string(sb[:4]) != "XFSB" {
		return fmt.Errorf("%w: XFS superblock magic missing", ErrCorruptFilesystem)
	}
	r.blockSize = uint64(binary.BigEndian.Uint32(sb[4:8]))
	r.dblocks = binary.BigEndian.Uint64(sb[8:16])
	r.rootInode = binary.BigEndian.Uint64(sb[56:64])
	r.agBlocks = uint64(binary.BigEndian.Uint32(sb[84:88]))
	r.agCount = binary.BigEndian.Uint32(sb[88:92])
	r.inodeSize = uint64(binary.BigEndian.Uint16(sb[104:106]))
	r.inodesPerBlock = uint64(binary.BigEndian.Uint16(sb[106:108]))
	r.inodePerBlockLog = sb[123]
	r.agBlockLog = sb[124]
	dirBlockLog := sb[192]
	// v4 filesystems advertise directory file types through features2, while
	// v5 filesystems use the incompat feature word.
	r.hasFileType = binary.BigEndian.Uint32(sb[200:204])&0x200 != 0 || binary.BigEndian.Uint32(sb[216:220])&1 != 0
	if r.blockSize < 512 || r.blockSize > 64*1024 || r.blockSize&(r.blockSize-1) != 0 || r.dblocks == 0 || r.agBlocks == 0 || r.agCount == 0 || r.inodeSize < 256 || r.inodeSize > 2048 || r.inodesPerBlock == 0 || r.inodesPerBlock*r.inodeSize != r.blockSize || uint64(1)<<r.inodePerBlockLog != r.inodesPerBlock || r.agBlockLog > 31 || dirBlockLog > 8 {
		return fmt.Errorf("%w: inconsistent XFS superblock geometry", ErrCorruptFilesystem)
	}
	if r.dblocks > r.volume.SizeBytes/r.blockSize || uint64(r.agCount)*r.agBlocks < r.dblocks || r.rootInode == 0 {
		return fmt.Errorf("%w: XFS geometry exceeds volume", ErrCorruptFilesystem)
	}
	r.dirBlockSize = r.blockSize << dirBlockLog
	if r.dirBlockSize > maxMappedRegionSize {
		return fmt.Errorf("%w: XFS directory block is too large", ErrCorruptFilesystem)
	}
	return nil
}

func (r *xfsReader) inode(number uint64) (xfsInode, error) {
	aginoBits := uint(r.agBlockLog) + uint(r.inodePerBlockLog)
	if aginoBits >= 63 {
		return xfsInode{}, fmt.Errorf("%w: XFS inode geometry overflows", ErrCorruptFilesystem)
	}
	agno := number >> aginoBits
	agino := number & ((uint64(1) << aginoBits) - 1)
	agbno := agino >> r.inodePerBlockLog
	offsetInBlock := agino & (r.inodesPerBlock - 1)
	if agno >= uint64(r.agCount) || agbno >= r.agBlocks {
		return xfsInode{}, fmt.Errorf("%w: XFS inode %d is outside allocation groups", ErrCorruptFilesystem, number)
	}
	fsblock := agno*r.agBlocks + agbno
	raw, err := r.read(fsblock*r.blockSize+offsetInBlock*r.inodeSize, r.inodeSize)
	if err != nil {
		return xfsInode{}, err
	}
	if string(raw[:2]) != "IN" || raw[4] < 2 || raw[4] > 3 {
		return xfsInode{}, fmt.Errorf("%w: XFS inode %d header is invalid", ErrCorruptFilesystem, number)
	}
	coreSize := 100
	if raw[4] == 3 {
		coreSize = 176
	}
	if coreSize > len(raw) {
		return xfsInode{}, fmt.Errorf("%w: XFS inode core is truncated", ErrCorruptFilesystem)
	}
	forkOffset := int(raw[82]) * 8
	dataEnd := len(raw)
	if forkOffset != 0 {
		if forkOffset < coreSize || forkOffset > len(raw) {
			return xfsInode{}, fmt.Errorf("%w: XFS inode fork offset is invalid", ErrCorruptFilesystem)
		}
		dataEnd = forkOffset
	}
	inode := xfsInode{number: number, mode: binary.BigEndian.Uint16(raw[2:4]), format: raw[5], uid: binary.BigEndian.Uint32(raw[8:12]), gid: binary.BigEndian.Uint32(raw[12:16]), size: binary.BigEndian.Uint64(raw[56:64]), nextents: binary.BigEndian.Uint32(raw[76:80]), data: append([]byte(nil), raw[coreSize:dataEnd]...)}
	if inode.mode == 0 || inode.format < 1 || inode.format > 3 {
		return xfsInode{}, fmt.Errorf("%w: XFS inode %d is unallocated or has invalid format", ErrCorruptFilesystem, number)
	}
	return inode, nil
}

func (r *xfsReader) walkDirectory(number uint64, directoryPath string) error {
	if r.visitedDirs[number] {
		return fmt.Errorf("%w: XFS directory inode cycle at %d", ErrCorruptFilesystem, number)
	}
	r.visitedDirs[number] = true
	inode, err := r.inode(number)
	if err != nil {
		return err
	}
	if inode.mode&0xf000 != 0x4000 {
		return fmt.Errorf("%w: XFS inode %d is not a directory", ErrCorruptFilesystem, number)
	}
	var entries []xfsDirEntry
	switch inode.format {
	case 1:
		entries, err = parseXFSShortDirectory(inode.data, r.hasFileType)
	case 2:
		entries, err = r.readXFSDirectoryExtents(inode)
	default:
		err = fmt.Errorf("%w: XFS directory inode %d uses unsupported btree fork", ErrCorruptFilesystem, number)
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.name == "." || entry.name == ".." {
			continue
		}
		if entry.name == "" || strings.ContainsAny(entry.name, "/\\\x00") {
			return fmt.Errorf("%w: unsafe XFS filename", ErrCorruptFilesystem)
		}
		child, err := r.inode(entry.inode)
		if err != nil {
			return err
		}
		if len(r.inventory.Files) >= r.filesLimit {
			r.inventory.Truncated = true
			return nil
		}
		kind := inodeKind(child.mode)
		pathname := path.Join(directoryPath, entry.name)
		r.inventory.Files = append(r.inventory.Files, InventoryFile{Path: pathname, Type: kind, Mode: child.mode & 0x0fff, UID: child.uid, GID: child.gid, Size: child.size})
		if kind == "directory" {
			if err := r.walkDirectory(entry.inode, pathname); err != nil {
				return err
			}
		}
	}
	return nil
}

type xfsDirEntry struct {
	name  string
	inode uint64
}

func parseXFSShortDirectory(data []byte, hasFileType bool) ([]xfsDirEntry, error) {
	if len(data) < 6 {
		return nil, fmt.Errorf("%w: truncated XFS shortform directory", ErrCorruptFilesystem)
	}
	count, i8count := int(data[0]), int(data[1])
	inodeBytes := 4
	if i8count > 0 {
		inodeBytes = 8
	}
	offset := 2 + inodeBytes // skip parent inode
	if offset > len(data) {
		return nil, fmt.Errorf("%w: truncated XFS shortform parent", ErrCorruptFilesystem)
	}
	entries := make([]xfsDirEntry, 0, count)
	for i := 0; i < count; i++ {
		if offset+3 > len(data) {
			return nil, fmt.Errorf("%w: truncated XFS shortform entry", ErrCorruptFilesystem)
		}
		nameLength := int(data[offset])
		offset += 3 // namelen + directory offset
		if nameLength == 0 || offset+nameLength+inodeBytes > len(data) {
			return nil, fmt.Errorf("%w: invalid XFS shortform name length", ErrCorruptFilesystem)
		}
		name := string(data[offset : offset+nameLength])
		offset += nameLength
		if hasFileType {
			if offset >= len(data) {
				return nil, fmt.Errorf("%w: truncated XFS shortform file type", ErrCorruptFilesystem)
			}
			offset++
		}
		if offset+inodeBytes > len(data) {
			return nil, fmt.Errorf("%w: truncated XFS shortform inode", ErrCorruptFilesystem)
		}
		var inode uint64
		if inodeBytes == 8 {
			inode = binary.BigEndian.Uint64(data[offset : offset+8])
		} else {
			inode = uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
		}
		offset += inodeBytes
		entries = append(entries, xfsDirEntry{name, inode})
	}
	return entries, nil
}

type xfsExtent struct{ startOffset, startBlock, blockCount uint64 }

func parseXFSExtents(data []byte, count uint32, dblocks uint64) ([]xfsExtent, error) {
	if uint64(count)*16 > uint64(len(data)) {
		return nil, fmt.Errorf("%w: truncated XFS extent list", ErrCorruptFilesystem)
	}
	extents := make([]xfsExtent, 0, count)
	var previousEnd uint64
	for i := uint32(0); i < count; i++ {
		raw := data[i*16 : i*16+16]
		hi, lo := binary.BigEndian.Uint64(raw[:8]), binary.BigEndian.Uint64(raw[8:])
		startOffset := (hi & 0x7fffffffffffffff) >> 9
		startBlock := (hi&0x1ff)<<43 | lo>>21
		blockCount := lo & 0x1fffff
		if blockCount == 0 || startBlock >= dblocks || blockCount > dblocks-startBlock || i > 0 && startOffset < previousEnd {
			return nil, fmt.Errorf("%w: invalid or overlapping XFS extent", ErrCorruptFilesystem)
		}
		extents = append(extents, xfsExtent{startOffset, startBlock, blockCount})
		previousEnd = startOffset + blockCount
	}
	return extents, nil
}

func (r *xfsReader) readXFSDirectoryExtents(inode xfsInode) ([]xfsDirEntry, error) {
	extents, err := parseXFSExtents(inode.data, inode.nextents, r.dblocks)
	if err != nil {
		return nil, err
	}
	blocksPerDirectoryBlock := r.dirBlockSize / r.blockSize
	if blocksPerDirectoryBlock == 0 {
		return nil, fmt.Errorf("%w: invalid XFS directory block geometry", ErrCorruptFilesystem)
	}
	mapping := make(map[uint64]uint64)
	for _, extent := range extents {
		for block := uint64(0); block < extent.blockCount; block++ {
			logical := extent.startOffset + block
			if _, duplicate := mapping[logical]; duplicate {
				return nil, fmt.Errorf("%w: duplicate XFS logical directory block", ErrCorruptFilesystem)
			}
			mapping[logical] = extent.startBlock + block
		}
	}
	var entries []xfsDirEntry
	for logicalStart := uint64(0); logicalStart*r.blockSize < inode.size; logicalStart += blocksPerDirectoryBlock {
		physicalStart, ok := mapping[logicalStart]
		if !ok {
			continue // sparse directory region
		}
		raw := make([]byte, 0, r.dirBlockSize)
		for part := uint64(0); part < blocksPerDirectoryBlock; part++ {
			physical, exists := mapping[logicalStart+part]
			if !exists || physical != physicalStart+part {
				return nil, fmt.Errorf("%w: fragmented XFS directory block", ErrCorruptFilesystem)
			}
			chunk, readErr := r.read(physical*r.blockSize, r.blockSize)
			if readErr != nil {
				return nil, readErr
			}
			raw = append(raw, chunk...)
		}
		parsed, err := parseXFSDirectoryBlock(raw, r.hasFileType)
		if err != nil {
			return nil, err
		}
		entries = append(entries, parsed...)
	}
	return entries, nil
}

func parseXFSDirectoryBlock(raw []byte, hasFileType bool) ([]xfsDirEntry, error) {
	if len(raw) < 24 {
		return nil, fmt.Errorf("%w: truncated XFS directory block", ErrCorruptFilesystem)
	}
	magic := binary.BigEndian.Uint32(raw[:4])
	headerSize, end := 0, len(raw)
	switch magic {
	case 0x58443244: // XD2D data
		headerSize = 16
	case 0x58444433: // XDD3 data
		headerSize = 64
	case 0x58443242: // XD2B block
		headerSize = 16
		count := int(binary.BigEndian.Uint32(raw[len(raw)-8 : len(raw)-4]))
		end = len(raw) - 8 - count*8
	case 0x58444233: // XDB3 block
		headerSize = 64
		count := int(binary.BigEndian.Uint32(raw[len(raw)-8 : len(raw)-4]))
		end = len(raw) - 8 - count*8
	default:
		return nil, fmt.Errorf("%w: unsupported XFS directory block magic %#x", ErrCorruptFilesystem, magic)
	}
	if end < headerSize || end > len(raw) {
		return nil, fmt.Errorf("%w: invalid XFS directory leaf bounds", ErrCorruptFilesystem)
	}
	var entries []xfsDirEntry
	for offset := headerSize; offset+8 <= end; {
		if binary.BigEndian.Uint16(raw[offset:offset+2]) == 0xffff {
			if offset+4 > end {
				return nil, fmt.Errorf("%w: truncated XFS free region", ErrCorruptFilesystem)
			}
			length := int(binary.BigEndian.Uint16(raw[offset+2 : offset+4]))
			if length < 8 || offset+length > end {
				return nil, fmt.Errorf("%w: invalid XFS free region", ErrCorruptFilesystem)
			}
			offset += length
			continue
		}
		inode := binary.BigEndian.Uint64(raw[offset : offset+8])
		if offset+9 > end {
			return nil, fmt.Errorf("%w: truncated XFS directory entry", ErrCorruptFilesystem)
		}
		nameLength := int(raw[offset+8])
		fileTypeBytes := 0
		if hasFileType {
			fileTypeBytes = 1
		}
		entryLength := (8 + 1 + nameLength + fileTypeBytes + 2 + 7) &^ 7
		if nameLength == 0 || offset+entryLength > end {
			return nil, fmt.Errorf("%w: invalid XFS directory entry length", ErrCorruptFilesystem)
		}
		tagOffset := offset + 9 + nameLength + fileTypeBytes
		if tagOffset+2 > offset+entryLength || int(binary.BigEndian.Uint16(raw[tagOffset:tagOffset+2])) != offset {
			return nil, fmt.Errorf("%w: invalid XFS directory entry tag", ErrCorruptFilesystem)
		}
		entries = append(entries, xfsDirEntry{string(raw[offset+9 : offset+9+nameLength]), inode})
		offset += entryLength
	}
	return entries, nil
}

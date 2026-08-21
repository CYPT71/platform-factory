package vmdisk

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

var ErrCorruptFilesystem = errors.New("vmdisk: corrupt filesystem metadata")

const (
	maxInventoryFiles        = 10_000
	maxInventoryLogicalBytes = 256 << 20
	extentFlag               = 0x00080000
	extentMagic              = 0xf30a
	maxSemanticFileBytes     = 1 << 20
)

type FilesystemInventory struct {
	VolumeIndex         int             `json:"volume_index"`
	Filesystem          string          `json:"filesystem"`
	Files               []InventoryFile `json:"files"`
	FilesLimit          int             `json:"files_limit"`
	LogicalBytesRead    uint64          `json:"logical_bytes_read"`
	LogicalBytesLimit   uint64          `json:"logical_bytes_limit"`
	Truncated           bool            `json:"truncated"`
	UnsupportedFeatures []string        `json:"unsupported_features"`
}

// ReadExtFile reads one regular file by absolute path without following
// symlinks. It is intended for small semantic metadata such as os-release;
// files larger than 1 MiB are refused.
func ReadExtFile(diskPath string, format Format, volume Volume, target string) ([]byte, error) {
	return readExtFileWithLimit(diskPath, format, volume, target, maxSemanticFileBytes)
}

func readExtFileWithLimit(diskPath string, format Format, volume Volume, target string, limit uint64) ([]byte, error) {
	clean := path.Clean(target)
	if target == "" || clean != target || !strings.HasPrefix(clean, "/") || clean == "/" {
		return nil, fmt.Errorf("vmdisk: invalid absolute ext path %q", target)
	}
	ld, closer, err := openLogicalDisk(diskPath, format)
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	r := &extReader{ld: ld, volume: volume, filesLimit: maxInventoryFiles, bytesLimit: maxInventoryLogicalBytes, visitedDirectories: map[uint32]bool{}}
	if err := r.loadSuperblock(); err != nil {
		return nil, err
	}
	current := uint32(2)
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	for index, part := range parts {
		inodeNumber, err := r.lookupDirectoryEntry(current, part)
		if err != nil {
			return nil, err
		}
		inode, err := r.inode(inodeNumber)
		if err != nil {
			return nil, err
		}
		if index < len(parts)-1 {
			if inode.mode&0xf000 != 0x4000 {
				return nil, fmt.Errorf("%w: %s is not a directory", ErrCorruptFilesystem, strings.Join(parts[:index+1], "/"))
			}
			current = inodeNumber
			continue
		}
		if inode.mode&0xf000 != 0x8000 {
			return nil, fmt.Errorf("vmdisk: refuse to read non-regular ext path %s", target)
		}
		if inode.size > limit {
			return nil, fmt.Errorf("vmdisk: ext file %s exceeds %d-byte read limit", target, limit)
		}
		return r.inodeData(inode)
	}
	return nil, fs.ErrNotExist
}

type InventoryFile struct {
	Path               string `json:"path"`
	Type               string `json:"type"`
	Mode               uint16 `json:"mode"`
	UID                uint32 `json:"uid"`
	GID                uint32 `json:"gid"`
	Size               uint64 `json:"size"`
	ExtendedAttributes bool   `json:"extended_attributes"`
}

// InspectExtFilesystem inventories an ext2/3/4 volume without mounting it.
// Symlinks are recorded but never followed. All reads go through the bounded
// logical container mapper and are confined to the selected partition.
func InspectExtFilesystem(diskPath string, format Format, volume Volume) (FilesystemInventory, error) {
	ld, closer, err := openLogicalDisk(diskPath, format)
	if err != nil {
		return FilesystemInventory{}, err
	}
	defer closer.Close()
	reader := &extReader{ld: ld, volume: volume, filesLimit: maxInventoryFiles, bytesLimit: maxInventoryLogicalBytes, visitedDirectories: map[uint32]bool{}}
	if err := reader.loadSuperblock(); err != nil {
		return FilesystemInventory{}, err
	}
	result := FilesystemInventory{VolumeIndex: volume.Index, Filesystem: reader.filesystem, Files: []InventoryFile{}, FilesLimit: reader.filesLimit, LogicalBytesLimit: reader.bytesLimit}
	reader.inventory = &result
	if err := reader.walkDirectory(2, "/"); err != nil {
		return FilesystemInventory{}, err
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	result.LogicalBytesRead = reader.bytesRead
	return result, nil
}

type extReader struct {
	ld                 logicalDisk
	volume             Volume
	blockSize          uint64
	inodesCount        uint32
	blocksCount        uint64
	blocksPerGroup     uint32
	inodesPerGroup     uint32
	inodeSize          uint16
	firstDataBlock     uint32
	featureCompat      uint32
	featureIncompat    uint32
	descriptorSize     uint16
	filesystem         string
	filesLimit         int
	bytesLimit         uint64
	bytesRead          uint64
	visitedDirectories map[uint32]bool
	inventory          *FilesystemInventory
}

func (r *extReader) read(relative, length uint64) ([]byte, error) {
	if relative > r.volume.SizeBytes || length > r.volume.SizeBytes-relative {
		return nil, fmt.Errorf("%w: read [%d,%d) escapes volume %d", ErrCorruptFilesystem, relative, relative+length, r.volume.Index)
	}
	if length > maxMappedRegionSize || length > r.bytesLimit || r.bytesRead > r.bytesLimit-length {
		return nil, fmt.Errorf("%w: logical analysis exceeds %d bytes", ErrCorruptFilesystem, r.bytesLimit)
	}
	absolute := r.volume.StartBytes + relative
	if absolute < r.volume.StartBytes || absolute > uint64(^uint64(0)>>1) {
		return nil, fmt.Errorf("%w: volume offset overflows", ErrCorruptFilesystem)
	}
	data, err := r.ld.ReadLogical(int64(absolute), int64(length))
	if err != nil {
		return nil, err
	}
	r.bytesRead += length
	return data, nil
}

func (r *extReader) loadSuperblock() error {
	sb, err := r.read(1024, 1024)
	if err != nil {
		return err
	}
	if binary.LittleEndian.Uint16(sb[56:58]) != 0xef53 {
		return fmt.Errorf("%w: ext magic missing", ErrCorruptFilesystem)
	}
	logBlock := binary.LittleEndian.Uint32(sb[24:28])
	if logBlock > 6 {
		return fmt.Errorf("%w: ext block size exponent %d is invalid", ErrCorruptFilesystem, logBlock)
	}
	r.blockSize = uint64(1024) << logBlock
	r.inodesCount = binary.LittleEndian.Uint32(sb[0:4])
	r.blocksCount = uint64(binary.LittleEndian.Uint32(sb[4:8]))
	r.firstDataBlock = binary.LittleEndian.Uint32(sb[20:24])
	r.blocksPerGroup = binary.LittleEndian.Uint32(sb[32:36])
	r.inodesPerGroup = binary.LittleEndian.Uint32(sb[40:44])
	r.inodeSize = binary.LittleEndian.Uint16(sb[88:90])
	r.featureCompat = binary.LittleEndian.Uint32(sb[92:96])
	r.featureIncompat = binary.LittleEndian.Uint32(sb[96:100])
	if r.featureIncompat&0x80 != 0 {
		r.blocksCount |= uint64(binary.LittleEndian.Uint32(sb[0x150:0x154])) << 32
	}
	r.descriptorSize = 32
	if r.featureIncompat&0x80 != 0 {
		r.descriptorSize = binary.LittleEndian.Uint16(sb[254:256])
		if r.descriptorSize < 64 || r.descriptorSize > 1024 {
			return fmt.Errorf("%w: ext group descriptor size %d is invalid", ErrCorruptFilesystem, r.descriptorSize)
		}
	}
	if r.inodesCount < 2 || r.blocksCount == 0 || r.blocksPerGroup == 0 || r.inodesPerGroup == 0 || r.inodeSize < 128 || uint64(r.inodeSize) > r.blockSize {
		return fmt.Errorf("%w: inconsistent ext superblock geometry", ErrCorruptFilesystem)
	}
	if r.blocksCount > r.volume.SizeBytes/r.blockSize+1 {
		return fmt.Errorf("%w: ext block count exceeds partition", ErrCorruptFilesystem)
	}
	switch {
	case r.featureIncompat&0x40 != 0 || r.featureIncompat&0x80 != 0:
		r.filesystem = "ext4"
	case r.featureCompat&0x4 != 0:
		r.filesystem = "ext3"
	default:
		r.filesystem = "ext2"
	}
	return nil
}

type extInode struct {
	number uint32
	mode   uint16
	uid    uint32
	gid    uint32
	size   uint64
	flags  uint32
	blocks [60]byte
	xattrs bool
}

func (r *extReader) inode(number uint32) (extInode, error) {
	if number == 0 || number > r.inodesCount {
		return extInode{}, fmt.Errorf("%w: inode %d is outside inode table", ErrCorruptFilesystem, number)
	}
	index := number - 1
	group, within := index/r.inodesPerGroup, index%r.inodesPerGroup
	descriptorTableBlock := uint64(1)
	if r.blockSize == 1024 {
		descriptorTableBlock = 2
	}
	descriptorOffset := descriptorTableBlock*r.blockSize + uint64(group)*uint64(r.descriptorSize)
	descriptor, err := r.read(descriptorOffset, uint64(r.descriptorSize))
	if err != nil {
		return extInode{}, err
	}
	inodeTableBlock := uint64(binary.LittleEndian.Uint32(descriptor[8:12]))
	if r.featureIncompat&0x80 != 0 {
		inodeTableBlock |= uint64(binary.LittleEndian.Uint32(descriptor[40:44])) << 32
	}
	if inodeTableBlock == 0 || inodeTableBlock >= r.blocksCount {
		return extInode{}, fmt.Errorf("%w: inode table block is invalid", ErrCorruptFilesystem)
	}
	raw, err := r.read(inodeTableBlock*r.blockSize+uint64(within)*uint64(r.inodeSize), uint64(r.inodeSize))
	if err != nil {
		return extInode{}, err
	}
	inode := extInode{number: number, mode: binary.LittleEndian.Uint16(raw[0:2]), uid: uint32(binary.LittleEndian.Uint16(raw[2:4])), gid: uint32(binary.LittleEndian.Uint16(raw[24:26])), flags: binary.LittleEndian.Uint32(raw[32:36])}
	inode.size = uint64(binary.LittleEndian.Uint32(raw[4:8]))
	copy(inode.blocks[:], raw[40:100])
	if len(raw) >= 124 {
		inode.uid |= uint32(binary.LittleEndian.Uint16(raw[120:122])) << 16
		inode.gid |= uint32(binary.LittleEndian.Uint16(raw[122:124])) << 16
	}
	if inode.mode&0xf000 == 0x8000 && len(raw) >= 112 {
		inode.size |= uint64(binary.LittleEndian.Uint32(raw[108:112])) << 32
	}
	inode.xattrs = binary.LittleEndian.Uint32(raw[104:108]) != 0
	if inode.mode == 0 {
		return extInode{}, fmt.Errorf("%w: referenced inode %d is unallocated", ErrCorruptFilesystem, number)
	}
	return inode, nil
}

func (r *extReader) inodeData(inode extInode) ([]byte, error) {
	if inode.size > r.bytesLimit {
		return nil, fmt.Errorf("%w: inode %d size %d exceeds logical analysis limit", ErrCorruptFilesystem, inode.number, inode.size)
	}
	blocks, err := r.inodeBlocks(inode)
	if err != nil {
		return nil, err
	}
	data := make([]byte, 0, int(inode.size))
	remaining := inode.size
	for _, block := range blocks {
		if remaining == 0 {
			break
		}
		length := r.blockSize
		if remaining < length {
			length = remaining
		}
		if block == 0 {
			data = append(data, make([]byte, length)...)
		} else {
			if block >= r.blocksCount {
				return nil, fmt.Errorf("%w: inode %d references block %d outside filesystem", ErrCorruptFilesystem, inode.number, block)
			}
			chunk, err := r.read(block*r.blockSize, length)
			if err != nil {
				return nil, err
			}
			data = append(data, chunk...)
		}
		remaining -= length
	}
	if remaining != 0 {
		return nil, fmt.Errorf("%w: inode %d block map is shorter than file size", ErrCorruptFilesystem, inode.number)
	}
	return data, nil
}

func (r *extReader) inodeBlocks(inode extInode) ([]uint64, error) {
	if inode.flags&extentFlag != 0 {
		return r.extentBlocks(inode.blocks[:], inode.number, inode.size)
	}
	needed := (inode.size + r.blockSize - 1) / r.blockSize
	blocks := make([]uint64, 0, needed)
	for i := uint64(0); i < needed && i < 12; i++ {
		blocks = append(blocks, uint64(binary.LittleEndian.Uint32(inode.blocks[i*4:i*4+4])))
	}
	for depth, offset := range []int{48, 52, 56} {
		if uint64(len(blocks)) >= needed {
			break
		}
		root := uint64(binary.LittleEndian.Uint32(inode.blocks[offset : offset+4]))
		indirect, err := r.indirectBlocks(root, depth+1, needed-uint64(len(blocks)))
		if err != nil {
			return nil, fmt.Errorf("inode %d: %w", inode.number, err)
		}
		blocks = append(blocks, indirect...)
	}
	if uint64(len(blocks)) < needed {
		return nil, fmt.Errorf("%w: inode %d block map is too short", ErrCorruptFilesystem, inode.number)
	}
	return blocks, nil
}

func (r *extReader) indirectBlocks(block uint64, depth int, wanted uint64) ([]uint64, error) {
	if wanted == 0 {
		return nil, nil
	}
	pointersPerBlock := r.blockSize / 4
	capacity := uint64(1)
	for i := 0; i < depth; i++ {
		if capacity > ^uint64(0)/pointersPerBlock {
			capacity = ^uint64(0)
			break
		}
		capacity *= pointersPerBlock
	}
	if block == 0 {
		if wanted > capacity {
			wanted = capacity
		}
		return make([]uint64, int(wanted)), nil
	}
	if block >= r.blocksCount || depth < 1 || depth > 3 {
		return nil, fmt.Errorf("%w: invalid indirect block tree", ErrCorruptFilesystem)
	}
	raw, err := r.read(block*r.blockSize, r.blockSize)
	if err != nil {
		return nil, err
	}
	result := make([]uint64, 0, minUint64(wanted, capacity))
	for i := uint64(0); i < pointersPerBlock && uint64(len(result)) < wanted; i++ {
		pointer := uint64(binary.LittleEndian.Uint32(raw[i*4 : i*4+4]))
		if depth == 1 {
			result = append(result, pointer)
			continue
		}
		childrenPerPointer := capacity / pointersPerBlock
		remaining := wanted - uint64(len(result))
		if remaining > childrenPerPointer {
			remaining = childrenPerPointer
		}
		children, err := r.indirectBlocks(pointer, depth-1, remaining)
		if err != nil {
			return nil, err
		}
		result = append(result, children...)
	}
	return result, nil
}

func (r *extReader) extentBlocks(root []byte, inode uint32, size uint64) ([]uint64, error) {
	needed := (size + r.blockSize - 1) / r.blockSize
	blocks := make([]uint64, needed)
	visited := map[uint64]bool{}
	if err := r.parseExtentNode(root, -1, blocks, inode, visited); err != nil {
		return nil, err
	}
	return blocks, nil
}

func (r *extReader) parseExtentNode(node []byte, expectedDepth int, blocks []uint64, inode uint32, visited map[uint64]bool) error {
	if len(node) < 12 || binary.LittleEndian.Uint16(node[0:2]) != extentMagic {
		return fmt.Errorf("%w: inode %d has invalid extent header", ErrCorruptFilesystem, inode)
	}
	entries, capacity, depth := int(binary.LittleEndian.Uint16(node[2:4])), int(binary.LittleEndian.Uint16(node[4:6])), int(binary.LittleEndian.Uint16(node[6:8]))
	if expectedDepth >= 0 && depth != expectedDepth || entries > capacity || depth > 5 || 12+entries*12 > len(node) {
		return fmt.Errorf("%w: inode %d has invalid extent tree", ErrCorruptFilesystem, inode)
	}
	for i := 0; i < entries; i++ {
		entry := node[12+i*12 : 24+i*12]
		logical := uint64(binary.LittleEndian.Uint32(entry[0:4]))
		if depth == 0 {
			rawLength := binary.LittleEndian.Uint16(entry[4:6])
			length := uint64(rawLength & 0x7fff)
			physical := uint64(binary.LittleEndian.Uint16(entry[6:8]))<<32 | uint64(binary.LittleEndian.Uint32(entry[8:12]))
			if length == 0 || logical > uint64(len(blocks)) || length > uint64(len(blocks))-logical || physical > r.blocksCount || length > r.blocksCount-physical {
				return fmt.Errorf("%w: inode %d extent geometry is invalid", ErrCorruptFilesystem, inode)
			}
			for j := uint64(0); j < length; j++ {
				if rawLength&0x8000 != 0 { // uninitialized extent: logical zeros
					continue
				}
				if blocks[logical+j] != 0 {
					return fmt.Errorf("%w: inode %d has overlapping extents", ErrCorruptFilesystem, inode)
				}
				blocks[logical+j] = physical + j
			}
			continue
		}
		child := uint64(binary.LittleEndian.Uint16(entry[8:10]))<<32 | uint64(binary.LittleEndian.Uint32(entry[4:8]))
		if child == 0 || child >= r.blocksCount || visited[child] {
			return fmt.Errorf("%w: inode %d extent index is invalid or cyclic", ErrCorruptFilesystem, inode)
		}
		visited[child] = true
		raw, err := r.read(child*r.blockSize, r.blockSize)
		if err != nil {
			return err
		}
		if err := r.parseExtentNode(raw, depth-1, blocks, inode, visited); err != nil {
			return err
		}
	}
	return nil
}

func minUint64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func (r *extReader) walkDirectory(number uint32, directoryPath string) error {
	if r.visitedDirectories[number] {
		return fmt.Errorf("%w: directory inode cycle at %d", ErrCorruptFilesystem, number)
	}
	r.visitedDirectories[number] = true
	inode, err := r.inode(number)
	if err != nil {
		return err
	}
	if inode.mode&0xf000 != 0x4000 {
		return fmt.Errorf("%w: inode %d is not a directory", ErrCorruptFilesystem, number)
	}
	data, err := r.inodeData(inode)
	if err != nil {
		return err
	}
	for offset := 0; offset < len(data); {
		if len(data)-offset < 8 {
			return fmt.Errorf("%w: truncated directory entry", ErrCorruptFilesystem)
		}
		child := binary.LittleEndian.Uint32(data[offset : offset+4])
		recordLength := int(binary.LittleEndian.Uint16(data[offset+4 : offset+6]))
		nameLength := int(data[offset+6])
		if recordLength < 8 || recordLength%4 != 0 || recordLength > len(data)-offset || nameLength > recordLength-8 {
			return fmt.Errorf("%w: invalid directory record geometry", ErrCorruptFilesystem)
		}
		if child != 0 {
			name := string(data[offset+8 : offset+8+nameLength])
			if name != "." && name != ".." {
				if name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
					return fmt.Errorf("%w: unsafe directory name", ErrCorruptFilesystem)
				}
				childInode, err := r.inode(child)
				if err != nil {
					return err
				}
				childPath := path.Join(directoryPath, name)
				if !strings.HasPrefix(childPath, "/") {
					return fmt.Errorf("%w: inventory path escaped root", ErrCorruptFilesystem)
				}
				if len(r.inventory.Files) >= r.filesLimit {
					r.inventory.Truncated = true
					return nil
				}
				kind := inodeKind(childInode.mode)
				r.inventory.Files = append(r.inventory.Files, InventoryFile{Path: childPath, Type: kind, Mode: childInode.mode & 0x0fff, UID: childInode.uid, GID: childInode.gid, Size: childInode.size, ExtendedAttributes: childInode.xattrs})
				if kind == "directory" {
					if err := r.walkDirectory(child, childPath); err != nil {
						return err
					}
				}
			}
		}
		offset += recordLength
	}
	return nil
}

func (r *extReader) lookupDirectoryEntry(directory uint32, name string) (uint32, error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return 0, fmt.Errorf("vmdisk: unsafe ext path component %q", name)
	}
	inode, err := r.inode(directory)
	if err != nil {
		return 0, err
	}
	if inode.mode&0xf000 != 0x4000 {
		return 0, fmt.Errorf("%w: inode %d is not a directory", ErrCorruptFilesystem, directory)
	}
	data, err := r.inodeData(inode)
	if err != nil {
		return 0, err
	}
	for offset := 0; offset < len(data); {
		if len(data)-offset < 8 {
			return 0, fmt.Errorf("%w: truncated directory entry", ErrCorruptFilesystem)
		}
		child := binary.LittleEndian.Uint32(data[offset : offset+4])
		recordLength := int(binary.LittleEndian.Uint16(data[offset+4 : offset+6]))
		nameLength := int(data[offset+6])
		if recordLength < 8 || recordLength%4 != 0 || recordLength > len(data)-offset || nameLength > recordLength-8 {
			return 0, fmt.Errorf("%w: invalid directory record geometry", ErrCorruptFilesystem)
		}
		if child != 0 && string(data[offset+8:offset+8+nameLength]) == name {
			return child, nil
		}
		offset += recordLength
	}
	return 0, fmt.Errorf("%w: %s", fs.ErrNotExist, name)
}

func inodeKind(mode uint16) string {
	switch mode & 0xf000 {
	case 0x4000:
		return "directory"
	case 0x8000:
		return "file"
	case 0xa000:
		return "symlink"
	case 0x2000:
		return "character_device"
	case 0x6000:
		return "block_device"
	case 0x1000:
		return "fifo"
	case 0xc000:
		return "socket"
	default:
		return "unknown"
	}
}

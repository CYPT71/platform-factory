package vmdisk

import (
	"encoding/binary"
	"fmt"
	"path"
	"sort"
	"strings"
)

const (
	btrfsSuperOffset = 64 * 1024
	btrfsChunkItem   = 228
	btrfsRootItem    = 132
	btrfsInodeItem   = 1
	btrfsInodeRef    = 12
)

type btrfsChunk struct{ logical, length, physical uint64 }
type btrfsKey struct {
	objectID uint64
	itemType byte
	offset   uint64
}
type btrfsItem struct {
	key  btrfsKey
	data []byte
}

type btrfsReader struct {
	ld                    logicalDisk
	volume                Volume
	fsid                  [16]byte
	nodeSize              uint64
	rootTree, chunkTree   uint64
	rootLevel, chunkLevel byte
	chunks                []btrfsChunk
	bytesRead, bytesLimit uint64
	visited               map[uint64]bool
}

// InspectBtrfsFilesystem inventories a single-device Btrfs filesystem without
// mounting it. Mirrored chunks are accepted when one local stripe is readable;
// striped/parity profiles are rejected rather than reconstructed incorrectly.
func InspectBtrfsFilesystem(diskPath string, format Format, volume Volume) (FilesystemInventory, error) {
	ld, closer, err := openLogicalDisk(diskPath, format)
	if err != nil {
		return FilesystemInventory{}, err
	}
	defer closer.Close()
	r := &btrfsReader{ld: ld, volume: volume, bytesLimit: maxInventoryLogicalBytes, visited: map[uint64]bool{}}
	if err := r.loadSuperblock(); err != nil {
		return FilesystemInventory{}, err
	}
	if err := r.loadChunkTree(); err != nil {
		return FilesystemInventory{}, err
	}
	r.visited = map[uint64]bool{}
	rootItems, err := r.readTree(r.rootTree, r.rootLevel)
	if err != nil {
		return FilesystemInventory{}, err
	}
	var fsRoot uint64
	var fsLevel byte
	for _, item := range rootItems {
		if item.key.objectID == 5 && item.key.itemType == btrfsRootItem && len(item.data) >= 239 {
			fsRoot = binary.LittleEndian.Uint64(item.data[176:184])
			fsLevel = item.data[238]
		}
	}
	if fsRoot == 0 {
		return FilesystemInventory{}, fmt.Errorf("%w: Btrfs filesystem tree root missing", ErrCorruptFilesystem)
	}
	r.visited = map[uint64]bool{}
	items, err := r.readTree(fsRoot, fsLevel)
	if err != nil {
		return FilesystemInventory{}, err
	}
	result, err := btrfsInventory(items, volume.Index)
	if err != nil {
		return FilesystemInventory{}, err
	}
	result.LogicalBytesRead, result.LogicalBytesLimit, result.FilesLimit = r.bytesRead, r.bytesLimit, maxInventoryFiles
	return result, nil
}

func (r *btrfsReader) raw(relative, length uint64) ([]byte, error) {
	if relative > r.volume.SizeBytes || length > r.volume.SizeBytes-relative || length > maxMappedRegionSize || r.bytesRead > r.bytesLimit-length {
		return nil, fmt.Errorf("%w: Btrfs read outside volume or budget", ErrCorruptFilesystem)
	}
	b, err := r.ld.ReadLogical(int64(r.volume.StartBytes+relative), int64(length))
	if err == nil {
		r.bytesRead += length
	}
	return b, err
}

func (r *btrfsReader) loadSuperblock() error {
	sb, err := r.raw(btrfsSuperOffset, 4096)
	if err != nil {
		return err
	}
	if string(sb[64:72]) != "_BHRfS_M" {
		return fmt.Errorf("%w: Btrfs superblock magic missing", ErrCorruptFilesystem)
	}
	copy(r.fsid[:], sb[32:48])
	if binary.LittleEndian.Uint64(sb[48:56]) != btrfsSuperOffset {
		return fmt.Errorf("%w: Btrfs superblock address mismatch", ErrCorruptFilesystem)
	}
	r.rootTree, r.chunkTree = binary.LittleEndian.Uint64(sb[80:88]), binary.LittleEndian.Uint64(sb[88:96])
	r.nodeSize = uint64(binary.LittleEndian.Uint32(sb[148:152]))
	r.rootLevel, r.chunkLevel = sb[198], sb[199]
	if r.nodeSize < 4096 || r.nodeSize > 64*1024 || r.nodeSize&(r.nodeSize-1) != 0 || r.rootLevel > 8 || r.chunkLevel > 8 {
		return fmt.Errorf("%w: invalid Btrfs tree geometry", ErrCorruptFilesystem)
	}
	systemSize := int(binary.LittleEndian.Uint32(sb[160:164]))
	if systemSize < 0 || systemSize > 2048 || 811+systemSize > len(sb) {
		return fmt.Errorf("%w: invalid Btrfs system chunk array", ErrCorruptFilesystem)
	}
	for off := 811; off < 811+systemSize; {
		if off+17 > 811+systemSize {
			return fmt.Errorf("%w: truncated Btrfs system key", ErrCorruptFilesystem)
		}
		key := parseBtrfsKey(sb[off : off+17])
		off += 17
		if key.itemType != btrfsChunkItem {
			return fmt.Errorf("%w: non-chunk system item", ErrCorruptFilesystem)
		}
		chunk, used, err := parseBtrfsChunk(key.offset, sb[off:811+systemSize], r.volume.SizeBytes)
		if err != nil {
			return err
		}
		r.chunks = append(r.chunks, chunk)
		off += used
	}
	return nil
}

func parseBtrfsChunk(logical uint64, data []byte, volumeSize uint64) (btrfsChunk, int, error) {
	if len(data) < 48 {
		return btrfsChunk{}, 0, fmt.Errorf("%w: truncated Btrfs chunk", ErrCorruptFilesystem)
	}
	length, profile := binary.LittleEndian.Uint64(data[:8]), binary.LittleEndian.Uint64(data[24:32])
	stripes := int(binary.LittleEndian.Uint16(data[44:46]))
	used := 48 + stripes*32
	if length == 0 || stripes == 0 || used > len(data) {
		return btrfsChunk{}, 0, fmt.Errorf("%w: invalid Btrfs chunk geometry", ErrCorruptFilesystem)
	}
	// RAID0/10/5/6 need stripe reconstruction; SINGLE, DUP and RAID1 do not.
	if profile&(1<<3|1<<6|1<<7|1<<8) != 0 {
		return btrfsChunk{}, 0, fmt.Errorf("%w: unsupported striped Btrfs chunk profile", ErrCorruptFilesystem)
	}
	for i := 0; i < stripes; i++ {
		physical := binary.LittleEndian.Uint64(data[48+i*32+8 : 48+i*32+16])
		if physical <= volumeSize && length <= volumeSize-physical {
			return btrfsChunk{logical, length, physical}, used, nil
		}
	}
	return btrfsChunk{}, 0, fmt.Errorf("%w: no local Btrfs chunk stripe", ErrCorruptFilesystem)
}

func (r *btrfsReader) logical(address, length uint64) ([]byte, error) {
	for _, chunk := range r.chunks {
		if address >= chunk.logical && address-chunk.logical <= chunk.length && length <= chunk.length-(address-chunk.logical) {
			return r.raw(chunk.physical+address-chunk.logical, length)
		}
	}
	return nil, fmt.Errorf("%w: unmapped Btrfs logical address %#x", ErrCorruptFilesystem, address)
}

func (r *btrfsReader) loadChunkTree() error {
	r.visited = map[uint64]bool{}
	items, err := r.readTree(r.chunkTree, r.chunkLevel)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.key.itemType != btrfsChunkItem {
			continue
		}
		chunk, _, err := parseBtrfsChunk(item.key.offset, item.data, r.volume.SizeBytes)
		if err != nil {
			return err
		}
		r.chunks = append(r.chunks, chunk)
	}
	return nil
}

func (r *btrfsReader) readTree(address uint64, expectedLevel byte) ([]btrfsItem, error) {
	if r.visited[address] {
		return nil, fmt.Errorf("%w: Btrfs tree cycle", ErrCorruptFilesystem)
	}
	r.visited[address] = true
	raw, err := r.logical(address, r.nodeSize)
	if err != nil {
		return nil, err
	}
	if len(raw) < 101 || string(raw[32:48]) != string(r.fsid[:]) || binary.LittleEndian.Uint64(raw[48:56]) != address {
		return nil, fmt.Errorf("%w: invalid Btrfs tree header", ErrCorruptFilesystem)
	}
	n := int(binary.LittleEndian.Uint32(raw[96:100]))
	level := raw[100]
	if level != expectedLevel || level > 8 {
		return nil, fmt.Errorf("%w: inconsistent Btrfs tree level", ErrCorruptFilesystem)
	}
	var result []btrfsItem
	if level == 0 {
		if n > (len(raw)-101)/25 {
			return nil, fmt.Errorf("%w: excessive Btrfs leaf items", ErrCorruptFilesystem)
		}
		for i := 0; i < n; i++ {
			h := raw[101+i*25:]
			off, size := int(binary.LittleEndian.Uint32(h[17:21])), int(binary.LittleEndian.Uint32(h[21:25]))
			if off < 101+n*25 || size < 0 || off+size > len(raw) {
				return nil, fmt.Errorf("%w: invalid Btrfs leaf item bounds", ErrCorruptFilesystem)
			}
			result = append(result, btrfsItem{parseBtrfsKey(h[:17]), append([]byte(nil), raw[off:off+size]...)})
		}
		return result, nil
	}
	if n > (len(raw)-101)/33 {
		return nil, fmt.Errorf("%w: excessive Btrfs node pointers", ErrCorruptFilesystem)
	}
	for i := 0; i < n; i++ {
		p := raw[101+i*33:]
		child := binary.LittleEndian.Uint64(p[17:25])
		items, err := r.readTree(child, level-1)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
	}
	return result, nil
}

func parseBtrfsKey(raw []byte) btrfsKey {
	return btrfsKey{binary.LittleEndian.Uint64(raw[:8]), raw[8], binary.LittleEndian.Uint64(raw[9:17])}
}

func btrfsInventory(items []btrfsItem, volumeIndex int) (FilesystemInventory, error) {
	type inodeMeta struct {
		mode     uint32
		uid, gid uint32
		size     uint64
	}
	type ref struct {
		parent uint64
		name   string
	}
	inodes := map[uint64]inodeMeta{}
	refs := map[uint64]ref{}
	for _, item := range items {
		switch item.key.itemType {
		case btrfsInodeItem:
			if len(item.data) < 64 {
				return FilesystemInventory{}, fmt.Errorf("%w: truncated Btrfs inode item", ErrCorruptFilesystem)
			}
			inodes[item.key.objectID] = inodeMeta{binary.LittleEndian.Uint32(item.data[52:56]), binary.LittleEndian.Uint32(item.data[44:48]), binary.LittleEndian.Uint32(item.data[48:52]), binary.LittleEndian.Uint64(item.data[16:24])}
		case btrfsInodeRef:
			for off := 0; off < len(item.data); {
				if off+10 > len(item.data) {
					return FilesystemInventory{}, fmt.Errorf("%w: truncated Btrfs inode ref", ErrCorruptFilesystem)
				}
				n := int(binary.LittleEndian.Uint16(item.data[off+8 : off+10]))
				off += 10
				if n == 0 || off+n > len(item.data) {
					return FilesystemInventory{}, fmt.Errorf("%w: invalid Btrfs inode ref name", ErrCorruptFilesystem)
				}
				name := string(item.data[off : off+n])
				off += n
				if strings.ContainsAny(name, "/\\\x00") {
					return FilesystemInventory{}, fmt.Errorf("%w: unsafe Btrfs filename", ErrCorruptFilesystem)
				}
				refs[item.key.objectID] = ref{item.key.offset, name}
			}
		}
	}
	result := FilesystemInventory{VolumeIndex: volumeIndex, Filesystem: "btrfs", Files: []InventoryFile{}, UnsupportedFeatures: []string{"striped/parity chunks and subvolume traversal are rejected"}}
	for number, meta := range inodes {
		if number == 256 {
			continue
		}
		parts, current, seen := []string{}, number, map[uint64]bool{}
		for current != 256 {
			if seen[current] {
				return FilesystemInventory{}, fmt.Errorf("%w: Btrfs inode-ref cycle", ErrCorruptFilesystem)
			}
			seen[current] = true
			link, ok := refs[current]
			if !ok {
				parts = nil
				break
			}
			parts = append(parts, link.name)
			current = link.parent
		}
		if parts == nil {
			continue
		}
		for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
			parts[i], parts[j] = parts[j], parts[i]
		}
		result.Files = append(result.Files, InventoryFile{Path: path.Join("/", path.Join(parts...)), Type: inodeKind(uint16(meta.mode)), Mode: uint16(meta.mode) & 0xfff, UID: meta.uid, GID: meta.gid, Size: meta.size})
		if len(result.Files) >= maxInventoryFiles {
			result.Truncated = true
			break
		}
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	return result, nil
}

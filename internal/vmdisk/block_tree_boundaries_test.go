package vmdisk

import (
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type memoryLogical []byte

func (d memoryLogical) ReadLogical(offset, length int64) ([]byte, error) {
	if offset < 0 || length < 0 || offset > int64(len(d)) || length > int64(len(d))-offset {
		return nil, fmt.Errorf("read outside fixture")
	}
	return append([]byte(nil), d[offset:offset+length]...), nil
}
func TestExtIndirectTreesSparseFilesAndInvalidBlocks(t *testing.T) {
	for depth := 1; depth <= 3; depth++ {
		data := make(memoryLogical, 1024)
		for i := 1; i < depth; i++ {
			binary.LittleEndian.PutUint32(data[i*64:], uint32(i+1))
		}
		binary.LittleEndian.PutUint32(data[depth*64:], 4)
		binary.LittleEndian.PutUint32(data[depth*64+4:], 5)
		r := &extReader{ld: data, volume: Volume{SizeBytes: 1024}, blockSize: 64, blocksCount: 16, bytesLimit: 65536}
		blocks, err := r.indirectBlocks(1, depth, 2)
		if err != nil || !reflect.DeepEqual(blocks, []uint64{4, 5}) {
			t.Fatalf("depth %d: %v %v", depth, blocks, err)
		}
		blocks, err = r.indirectBlocks(0, depth, 1000)
		if err != nil || len(blocks) == 0 {
			t.Fatalf("sparse depth %d: %v", depth, err)
		}
		if _, err := r.indirectBlocks(16, depth, 1); !errors.Is(err, ErrCorruptFilesystem) {
			t.Fatal("escaped indirect block accepted")
		}
		if blocks, err := r.indirectBlocks(1, depth, 0); err != nil || len(blocks) != 0 {
			t.Fatal("zero-length traversal read blocks")
		}
		inode := extInode{number: 3, size: 400 * 64}
		content, err := r.inodeData(inode)
		if err != nil || len(content) != 400*64 {
			t.Fatalf("sparse indirect file: %d %v", len(content), err)
		}
		for _, b := range content {
			if b != 0 {
				t.Fatal("sparse file leaked physical bytes")
			}
		}
		inode.size = 64
		binary.LittleEndian.PutUint32(inode.blocks[:], 16)
		if _, err := r.inodeData(inode); !errors.Is(err, ErrCorruptFilesystem) {
			t.Fatal("escaped data block accepted")
		}
		inode.size = 65537
		if _, err := r.inodeData(inode); !errors.Is(err, ErrCorruptFilesystem) {
			t.Fatal("oversized file accepted")
		}
	}
}
func extentNode(depth uint16) []byte {
	node := make([]byte, 36)
	binary.LittleEndian.PutUint16(node, extentMagic)
	binary.LittleEndian.PutUint16(node[2:], 1)
	binary.LittleEndian.PutUint16(node[4:], 2)
	binary.LittleEndian.PutUint16(node[6:], depth)
	return node
}
func TestExtExtentTreesRejectOverlapCyclesAndUninitializedData(t *testing.T) {
	leaf := extentNode(0)
	binary.LittleEndian.PutUint16(leaf[16:], 2)
	binary.LittleEndian.PutUint32(leaf[20:], 4)
	data := make(memoryLogical, 1024)
	copy(data[64:], leaf)
	r := &extReader{ld: data, volume: Volume{SizeBytes: 1024}, blockSize: 64, blocksCount: 16, bytesLimit: 65536}
	blocks, err := r.extentBlocks(leaf, 3, 128)
	if err != nil || !reflect.DeepEqual(blocks, []uint64{4, 5}) {
		t.Fatalf("leaf: %v %v", blocks, err)
	}
	index := extentNode(1)
	binary.LittleEndian.PutUint32(index[16:], 1)
	blocks, err = r.extentBlocks(index, 3, 128)
	if err != nil || !reflect.DeepEqual(blocks, []uint64{4, 5}) {
		t.Fatalf("index: %v %v", blocks, err)
	}
	uninitialized := append([]byte(nil), leaf...)
	binary.LittleEndian.PutUint16(uninitialized[16:], 0x8002)
	blocks, err = r.extentBlocks(uninitialized, 3, 128)
	if err != nil || !reflect.DeepEqual(blocks, []uint64{0, 0}) {
		t.Fatal("uninitialized extent exposed bytes")
	}
	overlap := append([]byte(nil), leaf...)
	binary.LittleEndian.PutUint16(overlap[2:], 2)
	copy(overlap[24:], leaf[12:24])
	invalid := [][]byte{nil, make([]byte, 12), overlap}
	badDepth := append([]byte(nil), leaf...)
	binary.LittleEndian.PutUint16(badDepth[6:], 6)
	invalid = append(invalid, badDepth)
	badLength := append([]byte(nil), leaf...)
	binary.LittleEndian.PutUint16(badLength[16:], 0)
	invalid = append(invalid, badLength)
	for _, node := range invalid {
		if _, err := r.extentBlocks(node, 3, 128); !errors.Is(err, ErrCorruptFilesystem) {
			t.Fatalf("bad extent accepted %x: %v", node, err)
		}
	}
	if err := r.parseExtentNode(index, -1, make([]uint64, 2), 3, map[uint64]bool{1: true}); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatal("cyclic index accepted")
	}
}
func TestBtrfsInternalTreeAndLeafBounds(t *testing.T) {
	data := make(memoryLogical, 12288)
	var fsid [16]byte
	copy(fsid[:], "bounded-fixture")
	// Internal node at 4096 points to an empty leaf at 8192.
	node := data[4096:8192]
	writeBtrfsLeaf(node, fsid, 4096, nil)
	node[100] = 1
	binary.LittleEndian.PutUint32(node[96:], 1)
	binary.LittleEndian.PutUint64(node[118:], 8192)
	writeBtrfsLeaf(data[8192:], fsid, 8192, nil)
	makeReader := func() *btrfsReader {
		return &btrfsReader{ld: data, volume: Volume{SizeBytes: uint64(len(data))}, nodeSize: 4096, fsid: fsid, chunks: []btrfsChunk{{logical: 0, length: uint64(len(data)), physical: 0}}, bytesLimit: 65536, visited: map[uint64]bool{}}
	}
	if items, err := makeReader().readTree(4096, 1); err != nil || len(items) != 0 {
		t.Fatalf("internal tree: %v %v", items, err)
	}
	r := makeReader()
	r.visited[4096] = true
	if _, err := r.readTree(4096, 1); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatal("tree cycle accepted")
	}
	if _, err := makeReader().logical(1<<30, 1); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatal("unmapped address accepted")
	}
	original := append([]byte(nil), node...)
	for _, mutate := range []func([]byte){
		func(n []byte) { n[32] ^= 1 }, func(n []byte) { n[100] = 2 }, func(n []byte) { binary.LittleEndian.PutUint32(n[96:], 0xffffffff) },
	} {
		copy(node, original)
		mutate(node)
		if _, err := makeReader().readTree(4096, 1); !errors.Is(err, ErrCorruptFilesystem) {
			t.Fatal("corrupt internal tree accepted")
		}
	}
	for _, item := range []btrfsItem{{key: btrfsKey{objectID: 257, itemType: btrfsInodeItem}, data: []byte{1}}, {key: btrfsKey{objectID: 257, itemType: btrfsInodeRef}, data: []byte{1}}, {key: btrfsKey{objectID: 257, itemType: btrfsInodeRef}, data: make([]byte, 10)}} {
		if _, err := btrfsInventory([]btrfsItem{item}, 0); !errors.Is(err, ErrCorruptFilesystem) {
			t.Fatal("truncated inode metadata accepted")
		}
	}
}
func TestXFSDirectoryAndExtentStructuralBounds(t *testing.T) {
	for _, raw := range [][]byte{nil, make([]byte, 5), {1, 1, 0, 0, 0, 0}, {1, 0, 0, 0, 0, 0}, {1, 0, 0, 0, 0, 0, 0, 0, 0}} {
		if _, err := parseXFSShortDirectory(raw, true); !errors.Is(err, ErrCorruptFilesystem) {
			t.Fatalf("bad short directory accepted %x", raw)
		}
	}
	// One 64-bit shortform inode, no file-type byte.
	short := make([]byte, 2+8+3+1+8)
	short[0], short[1], short[10] = 1, 1, 1
	short[13] = 'x'
	binary.BigEndian.PutUint64(short[14:], 42)
	entries, err := parseXFSShortDirectory(short, false)
	if err != nil || len(entries) != 1 || entries[0].inode != 42 {
		t.Fatalf("64-bit shortform: %v %v", entries, err)
	}
	for _, magic := range []uint32{0, 0x58443244, 0x58444433, 0x58443242, 0x58444233} {
		raw := make([]byte, 128)
		binary.BigEndian.PutUint32(raw, magic)
		// Zero name/invalid free region or impossible trailing leaf count.
		if magic == 0x58443242 || magic == 0x58444233 {
			binary.BigEndian.PutUint32(raw[120:], 0xffffffff)
		}
		if _, err := parseXFSDirectoryBlock(raw, true); !errors.Is(err, ErrCorruptFilesystem) {
			t.Fatalf("bad block %#x accepted", magic)
		}
	}
	if _, err := parseXFSExtents(nil, 1, 10); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatal("truncated extent accepted")
	}
	if _, err := parseXFSExtents(make([]byte, 16), 1, 10); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatal("zero extent accepted")
	}
}

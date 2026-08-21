package vmdisk

import (
	"encoding/binary"
	"errors"
	"testing"
)

func TestInspectBtrfsFilesystemInventoriesInodeRefs(t *testing.T) {
	disk, volume := buildBtrfsFixture(t)
	inventory, err := InspectBtrfsFilesystem(disk, FormatRAW, volume)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Filesystem != "btrfs" || len(inventory.Files) != 1 || inventory.Files[0].Path != "/hello" || inventory.Files[0].Size != 5 {
		t.Fatalf("unexpected inventory: %#v", inventory)
	}
	if inventory.LogicalBytesRead == 0 || inventory.LogicalBytesRead > inventory.LogicalBytesLimit {
		t.Fatalf("invalid bounds: %#v", inventory)
	}
}

func TestBtrfsInventoryRejectsParentCycles(t *testing.T) {
	inode := make([]byte, 64)
	binary.LittleEndian.PutUint32(inode[52:56], 0x81a4)
	ref := make([]byte, 11)
	binary.LittleEndian.PutUint16(ref[8:10], 1)
	ref[10] = 'x'
	_, err := btrfsInventory([]btrfsItem{{btrfsKey{257, btrfsInodeItem, 0}, inode}, {btrfsKey{257, btrfsInodeRef, 257}, ref}}, 0)
	if !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatalf("err=%v want ErrCorruptFilesystem", err)
	}
}

func TestParseBtrfsChunkRejectsStripedProfiles(t *testing.T) {
	data := make([]byte, 80)
	binary.LittleEndian.PutUint64(data[:8], 4096)
	binary.LittleEndian.PutUint64(data[24:32], 1<<3)
	binary.LittleEndian.PutUint16(data[44:46], 1)
	if _, _, err := parseBtrfsChunk(0, data, 8192); !errors.Is(err, ErrCorruptFilesystem) {
		t.Fatalf("err=%v", err)
	}
}

func buildBtrfsFixture(t *testing.T) (string, Volume) {
	t.Helper()
	const nodeSize, imageSize = 4096, 2 * 1024 * 1024
	const rootTree, chunkTree, fsTree = 0x20000, 0x30000, 0x40000
	image := make([]byte, imageSize)
	var fsid [16]byte
	copy(fsid[:], "fixture-btrfs-id")
	sb := image[btrfsSuperOffset : btrfsSuperOffset+4096]
	copy(sb[32:48], fsid[:])
	binary.LittleEndian.PutUint64(sb[48:56], btrfsSuperOffset)
	copy(sb[64:72], "_BHRfS_M")
	binary.LittleEndian.PutUint64(sb[80:88], rootTree)
	binary.LittleEndian.PutUint64(sb[88:96], chunkTree)
	binary.LittleEndian.PutUint64(sb[112:120], imageSize)
	binary.LittleEndian.PutUint32(sb[144:148], 4096)
	binary.LittleEndian.PutUint32(sb[148:152], nodeSize)
	sb[198], sb[199] = 0, 0
	chunkData := make([]byte, 80)
	binary.LittleEndian.PutUint64(chunkData[:8], imageSize)
	binary.LittleEndian.PutUint16(chunkData[44:46], 1)
	binary.LittleEndian.PutUint64(chunkData[48:56], 1)
	binary.LittleEndian.PutUint64(chunkData[56:64], 0)
	writeBtrfsKey(sb[811:828], btrfsKey{256, btrfsChunkItem, 0})
	copy(sb[828:908], chunkData)
	binary.LittleEndian.PutUint32(sb[160:164], 97)

	writeBtrfsLeaf(image[rootTree:rootTree+nodeSize], fsid, rootTree, []btrfsItem{{key: btrfsKey{5, btrfsRootItem, 1}, data: func() []byte { b := make([]byte, 239); binary.LittleEndian.PutUint64(b[176:184], fsTree); return b }()}})
	writeBtrfsLeaf(image[chunkTree:chunkTree+nodeSize], fsid, chunkTree, nil)
	rootInode := make([]byte, 64)
	binary.LittleEndian.PutUint32(rootInode[52:56], 0x41ed)
	fileInode := make([]byte, 64)
	binary.LittleEndian.PutUint64(fileInode[16:24], 5)
	binary.LittleEndian.PutUint32(fileInode[44:48], 1000)
	binary.LittleEndian.PutUint32(fileInode[48:52], 1000)
	binary.LittleEndian.PutUint32(fileInode[52:56], 0x81a4)
	ref := make([]byte, 15)
	binary.LittleEndian.PutUint16(ref[8:10], 5)
	copy(ref[10:], "hello")
	writeBtrfsLeaf(image[fsTree:fsTree+nodeSize], fsid, fsTree, []btrfsItem{{btrfsKey{256, btrfsInodeItem, 0}, rootInode}, {btrfsKey{257, btrfsInodeItem, 0}, fileInode}, {btrfsKey{257, btrfsInodeRef, 256}, ref}})
	return writeTemp(t, "btrfs.raw", image), Volume{Index: 0, SizeBytes: imageSize, Content: "unsupported-filesystem:btrfs"}
}

func writeBtrfsLeaf(raw []byte, fsid [16]byte, address uint64, items []btrfsItem) {
	copy(raw[32:48], fsid[:])
	binary.LittleEndian.PutUint64(raw[48:56], address)
	binary.LittleEndian.PutUint32(raw[96:100], uint32(len(items)))
	offset := len(raw)
	for i, item := range items {
		offset -= len(item.data)
		copy(raw[offset:], item.data)
		header := raw[101+i*25:]
		writeBtrfsKey(header[:17], item.key)
		binary.LittleEndian.PutUint32(header[17:21], uint32(offset))
		binary.LittleEndian.PutUint32(header[21:25], uint32(len(item.data)))
	}
}

func writeBtrfsKey(raw []byte, key btrfsKey) {
	binary.LittleEndian.PutUint64(raw[:8], key.objectID)
	raw[8] = key.itemType
	binary.LittleEndian.PutUint64(raw[9:17], key.offset)
}

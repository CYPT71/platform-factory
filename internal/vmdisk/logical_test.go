package vmdisk

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func createTemp(t *testing.T, name string, size int) (*os.File, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if size > 0 {
		if err := file.Truncate(int64(size)); err != nil {
			t.Fatalf("truncate: %v", err)
		}
	}
	t.Cleanup(func() { file.Close() })
	return file, path
}

func putBE32(b []byte, off int, v uint32) {
	b[off] = byte(v >> 24)
	b[off+1] = byte(v >> 16)
	b[off+2] = byte(v >> 8)
	b[off+3] = byte(v)
}
func putBE64(b []byte, off int, v uint64) {
	for i := 0; i < 8; i++ {
		b[off+7-i] = byte(v >> (8 * i))
	}
}
func putLE32(b []byte, off int, v uint32) {
	b[off] = byte(v)
	b[off+1] = byte(v >> 8)
	b[off+2] = byte(v >> 16)
	b[off+3] = byte(v >> 24)
}
func putLE64(b []byte, off int, v uint64) {
	for i := 0; i < 8; i++ {
		b[off+i] = byte(v >> (8 * i))
	}
}

func TestRawLogicalDiskReadsDirectly(t *testing.T) {
	file, _ := createTemp(t, "raw.bin", 0)
	content := []byte("hello-raw-disk-content")
	if _, err := file.WriteAt(content, 100); err != nil {
		t.Fatalf("write: %v", err)
	}
	ld := &rawLogicalDisk{file: file}
	got, err := ld.ReadLogical(100, int64(len(content)))
	if err != nil {
		t.Fatalf("ReadLogical: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("got %q, want %q", got, content)
	}
}

func TestVHDFixedLogicalDiskReadsDirectly(t *testing.T) {
	const dataSize = 8192
	file, _ := createTemp(t, "fixed.vhd", dataSize+vhdFooterSize)
	payload := bytes.Repeat([]byte{0xAB}, 512)
	if _, err := file.WriteAt(payload, 0); err != nil {
		t.Fatalf("write: %v", err)
	}
	footer := make([]byte, vhdFooterSize)
	copy(footer[:8], "conectix")
	putBE32(footer, 60, vhdDiskFixed)
	if _, err := file.WriteAt(footer, dataSize); err != nil {
		t.Fatalf("write footer: %v", err)
	}

	ld, err := newVHDLogicalDisk(file)
	if err != nil {
		t.Fatalf("newVHDLogicalDisk: %v", err)
	}
	if !ld.fixed {
		t.Fatal("expected fixed VHD")
	}
	got, err := ld.ReadLogical(0, 512)
	if err != nil {
		t.Fatalf("ReadLogical: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %x, want %x", got[:8], payload[:8])
	}
}

// buildDynamicVHD writes a minimal, spec-shaped dynamic VHD with two
// blocks: block 0 allocated with block0Content (defaults to a repeated
// byte pattern if nil), block 1 left unallocated (BAT entry
// 0xFFFFFFFF).
func buildDynamicVHD(t *testing.T, block0Content ...[]byte) (*os.File, []byte) {
	t.Helper()
	const blockSize = 4096 // 8 sectors, small for a fast test
	const dynHeaderOffset = 0
	const dynHeaderSize = 1024
	// 20 blocks comfortably covers partitionScanWindow (64 KiB) so a
	// full-window ScanBootPartition read stays within the BAT.
	const maxEntries = 20
	batOffset := int64(dynHeaderOffset + dynHeaderSize)
	batBytes := int64(maxEntries) * 4
	bitmapSectorBytes := int64(512) // 1 sector covers an 8-sector block's bitmap easily
	block0DataOffsetSector := (batOffset + batBytes + 511) / 512
	block0BitmapOffset := block0DataOffsetSector * 512
	block0DataOffset := block0BitmapOffset + bitmapSectorBytes
	fileSize := block0DataOffset + blockSize + vhdFooterSize

	file, _ := createTemp(t, "dynamic.vhd", int(fileSize))

	dynHeader := make([]byte, dynHeaderSize)
	copy(dynHeader[:8], "cxsparse")
	putBE64(dynHeader, 16, uint64(batOffset))
	putBE32(dynHeader, 28, maxEntries)
	putBE32(dynHeader, 32, blockSize)
	if _, err := file.WriteAt(dynHeader, dynHeaderOffset); err != nil {
		t.Fatalf("write dynamic header: %v", err)
	}

	bat := bytes.Repeat([]byte{0xFF}, int(batBytes)) // every entry starts unallocated (0xFFFFFFFF)
	putBE32(bat, 0, uint32(block0BitmapOffset/512))  // block 0: allocated, sector offset
	if _, err := file.WriteAt(bat, batOffset); err != nil {
		t.Fatalf("write bat: %v", err)
	}

	block0 := bytes.Repeat([]byte{0xCD}, blockSize)
	if len(block0Content) == 1 {
		block0 = block0Content[0]
	}
	if _, err := file.WriteAt(block0, block0DataOffset); err != nil {
		t.Fatalf("write block0: %v", err)
	}

	footer := make([]byte, vhdFooterSize)
	copy(footer[:8], "conectix")
	putBE32(footer, 60, vhdDiskDynamic)
	putBE64(footer, 16, uint64(dynHeaderOffset))
	if _, err := file.WriteAt(footer, fileSize-vhdFooterSize); err != nil {
		t.Fatalf("write footer: %v", err)
	}
	return file, block0
}

func TestVHDDynamicLogicalDiskMapsAllocatedBlock(t *testing.T) {
	file, block0 := buildDynamicVHD(t)
	ld, err := newVHDLogicalDisk(file)
	if err != nil {
		t.Fatalf("newVHDLogicalDisk: %v", err)
	}
	if ld.fixed {
		t.Fatal("expected dynamic VHD")
	}
	got, err := ld.ReadLogical(0, 4096)
	if err != nil {
		t.Fatalf("ReadLogical: %v", err)
	}
	if !bytes.Equal(got, block0) {
		t.Fatalf("block0 mismatch")
	}
}

func TestVHDDynamicLogicalDiskUnallocatedBlockReadsZero(t *testing.T) {
	file, _ := buildDynamicVHD(t)
	ld, err := newVHDLogicalDisk(file)
	if err != nil {
		t.Fatalf("newVHDLogicalDisk: %v", err)
	}
	got, err := ld.ReadLogical(4096, 4096) // block 1: unallocated
	if err != nil {
		t.Fatalf("ReadLogical: %v", err)
	}
	if !bytes.Equal(got, make([]byte, 4096)) {
		t.Fatalf("expected zero-filled unallocated block")
	}
}

// buildQCOW2 writes a minimal spec-shaped QCOW2 image with one L1
// entry pointing at one L2 table, whose first entry points at an
// allocated cluster; a second L2 entry is left unallocated.
func buildQCOW2(t *testing.T) (*os.File, []byte) {
	t.Helper()
	const clusterBits = 12 // 4096-byte clusters
	const clusterSize = 1 << clusterBits
	const l1Offset = int64(clusterSize) // cluster-align for realism
	const l2Offset = l1Offset + clusterSize
	const dataClusterOffset = l2Offset + clusterSize

	fileSize := dataClusterOffset + clusterSize
	file, _ := createTemp(t, "image.qcow2", int(fileSize))

	header := make([]byte, 48)
	copy(header[0:4], []byte{0x51, 0x46, 0x49, 0xfb})
	putBE32(header, 4, 3) // version 3
	putBE32(header, 20, clusterBits)
	putBE32(header, 36, 1) // l1_size = 1
	putBE64(header, 40, uint64(l1Offset))
	if _, err := file.WriteAt(header, 0); err != nil {
		t.Fatalf("write header: %v", err)
	}

	l1 := make([]byte, 8)
	putBE64(l1, 0, uint64(l2Offset)|qcow2OFlagCopied)
	if _, err := file.WriteAt(l1, l1Offset); err != nil {
		t.Fatalf("write l1: %v", err)
	}

	l2 := make([]byte, clusterSize)
	putBE64(l2, 0, uint64(dataClusterOffset)|qcow2OFlagCopied) // entry 0: allocated
	// entry 1 (offset 8): left zero -> unallocated
	if _, err := file.WriteAt(l2, l2Offset); err != nil {
		t.Fatalf("write l2: %v", err)
	}

	cluster := bytes.Repeat([]byte{0xEF}, clusterSize)
	if _, err := file.WriteAt(cluster, dataClusterOffset); err != nil {
		t.Fatalf("write cluster: %v", err)
	}
	return file, cluster
}

func TestQCOW2LogicalDiskMapsAllocatedCluster(t *testing.T) {
	file, cluster := buildQCOW2(t)
	ld, err := newQCOW2LogicalDisk(file)
	if err != nil {
		t.Fatalf("newQCOW2LogicalDisk: %v", err)
	}
	got, err := ld.ReadLogical(0, 4096)
	if err != nil {
		t.Fatalf("ReadLogical: %v", err)
	}
	if !bytes.Equal(got, cluster) {
		t.Fatalf("cluster mismatch")
	}
}

func TestQCOW2LogicalDiskRejectsSelfReferentialMetadata(t *testing.T) {
	file, _ := buildQCOW2(t)
	const clusterSize = int64(4096)
	const l1Offset = clusterSize
	const l2Offset = l1Offset + clusterSize
	entry := make([]byte, 8)
	putBE64(entry, 0, uint64(l2Offset)|qcow2OFlagCopied)
	if _, err := file.WriteAt(entry, l2Offset); err != nil {
		t.Fatal(err)
	}
	ld, err := newQCOW2LogicalDisk(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ld.ReadLogical(0, 512); !errors.Is(err, ErrCannotMapLogicalDisk) {
		t.Fatalf("self-referential L2 data pointer err=%v, want ErrCannotMapLogicalDisk", err)
	}
}

func TestQCOW2LogicalDiskRejectsTruncatedL1Table(t *testing.T) {
	file, _ := createTemp(t, "truncated-l1.qcow2", 4096)
	header := make([]byte, 72)
	copy(header[0:4], []byte{0x51, 0x46, 0x49, 0xfb})
	putBE32(header, 4, 3)
	putBE32(header, 20, 12)
	putBE32(header, 36, 2)
	putBE64(header, 40, 4096)
	if _, err := file.WriteAt(header, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := newQCOW2LogicalDisk(file); !errors.Is(err, ErrCannotMapLogicalDisk) {
		t.Fatalf("truncated L1 err=%v, want ErrCannotMapLogicalDisk", err)
	}
}

func TestQCOW2LogicalDiskUnallocatedClusterReadsZero(t *testing.T) {
	file, _ := buildQCOW2(t)
	ld, err := newQCOW2LogicalDisk(file)
	if err != nil {
		t.Fatalf("newQCOW2LogicalDisk: %v", err)
	}
	got, err := ld.ReadLogical(4096, 4096) // second cluster: unallocated L2 entry
	if err != nil {
		t.Fatalf("ReadLogical: %v", err)
	}
	if !bytes.Equal(got, make([]byte, 4096)) {
		t.Fatalf("expected zero-filled unallocated cluster")
	}
}

func TestQCOW2LogicalDiskRejectsCompressedCluster(t *testing.T) {
	file, _ := buildQCOW2(t)
	// Flip the compressed bit on L1's L2 table pointer's referenced L2
	// entry 0 by rewriting it with the compressed flag set instead of copied.
	const clusterBits = 12
	const clusterSize = 1 << clusterBits
	const l1Offset = int64(clusterSize)
	const l2Offset = l1Offset + clusterSize
	const dataClusterOffset = l2Offset + clusterSize
	l2entry := make([]byte, 8)
	putBE64(l2entry, 0, uint64(dataClusterOffset)|qcow2OFlagCompressed)
	if _, err := file.WriteAt(l2entry, l2Offset); err != nil {
		t.Fatalf("write l2 entry: %v", err)
	}
	ld, err := newQCOW2LogicalDisk(file)
	if err != nil {
		t.Fatalf("newQCOW2LogicalDisk: %v", err)
	}
	if _, err := ld.ReadLogical(0, 4096); !errors.Is(err, ErrCannotMapLogicalDisk) {
		t.Fatalf("err = %v, want ErrCannotMapLogicalDisk", err)
	}
}

// buildVMDK writes a minimal spec-shaped binary-sparse VMDK: one Grain
// Directory entry pointing at one Grain Table, whose first entry maps
// an allocated grain; a second grain table entry is left unallocated.
func buildVMDK(t *testing.T) (*os.File, []byte) {
	t.Helper()
	const grainSectors = 8 // 4096-byte grains, small for a fast test
	const grainBytes = grainSectors * 512
	const gdOffsetSectors = 2 // arbitrary, sector-aligned
	const gtOffsetSectors = gdOffsetSectors + 1
	const grain0OffsetSectors = gtOffsetSectors + 1

	fileSize := (grain0OffsetSectors + grainSectors) * 512
	file, _ := createTemp(t, "sparse.vmdk", fileSize)

	header := make([]byte, 512)
	copy(header[0:4], "KDMV")
	putLE64(header, 20, grainSectors)
	putLE32(header, 44, 512) // numGTEsPerGT
	putLE64(header, 56, gdOffsetSectors)
	putLE64(header, 48, invalidVMDKOffset) // rgdOffset unused
	if _, err := file.WriteAt(header, 0); err != nil {
		t.Fatalf("write header: %v", err)
	}

	gd := make([]byte, 512)
	putLE32(gd, 0, gtOffsetSectors)
	if _, err := file.WriteAt(gd, gdOffsetSectors*512); err != nil {
		t.Fatalf("write gd: %v", err)
	}

	gt := make([]byte, 512)
	putLE32(gt, 0, grain0OffsetSectors) // grain 0: allocated
	// grain 1 (offset 4): left zero -> unallocated
	if _, err := file.WriteAt(gt, gtOffsetSectors*512); err != nil {
		t.Fatalf("write gt: %v", err)
	}

	grain0 := bytes.Repeat([]byte{0x5A}, grainBytes)
	if _, err := file.WriteAt(grain0, grain0OffsetSectors*512); err != nil {
		t.Fatalf("write grain0: %v", err)
	}
	return file, grain0
}

func TestVMDKLogicalDiskMapsAllocatedGrain(t *testing.T) {
	file, grain0 := buildVMDK(t)
	ld, err := newVMDKLogicalDisk(file)
	if err != nil {
		t.Fatalf("newVMDKLogicalDisk: %v", err)
	}
	got, err := ld.ReadLogical(0, int64(len(grain0)))
	if err != nil {
		t.Fatalf("ReadLogical: %v", err)
	}
	if !bytes.Equal(got, grain0) {
		t.Fatalf("grain0 mismatch")
	}
}

func TestVMDKLogicalDiskUnallocatedGrainReadsZero(t *testing.T) {
	file, grain0 := buildVMDK(t)
	ld, err := newVMDKLogicalDisk(file)
	if err != nil {
		t.Fatalf("newVMDKLogicalDisk: %v", err)
	}
	got, err := ld.ReadLogical(int64(len(grain0)), int64(len(grain0))) // grain 1: unallocated
	if err != nil {
		t.Fatalf("ReadLogical: %v", err)
	}
	if !bytes.Equal(got, make([]byte, len(grain0))) {
		t.Fatalf("expected zero-filled unallocated grain")
	}
}

func TestVMDKLogicalDiskRejectsTextDescriptor(t *testing.T) {
	file, _ := createTemp(t, "descriptor.vmdk", 0)
	if _, err := file.WriteAt([]byte("# Disk DescriptorFile\n"), 0); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := newVMDKLogicalDisk(file); !errors.Is(err, ErrCannotMapLogicalDisk) {
		t.Fatalf("err = %v, want ErrCannotMapLogicalDisk", err)
	}
}

// buildVHDX writes a minimal spec-shaped (non-differencing) VHDX: a
// region table pointing at a BAT and a Metadata region; the Metadata
// region carries one File Parameters item; the BAT has one fully
// present block and one not-present block.
func buildVHDX(t *testing.T) (*os.File, []byte) {
	t.Helper()
	const blockSize = 1 << 20 // 1 MiB - the BAT's own file-offset granularity, so block 0 can start right after the header region
	const metaRegionOffset = 3 * 1024 * 1024
	const batRegionOffset = 4 * 1024 * 1024
	const block0FileOffset = 5 * 1024 * 1024
	fileSize := block0FileOffset + 2*blockSize

	file, _ := createTemp(t, "image.vhdx", fileSize)
	if _, err := file.WriteAt([]byte("vhdxfile"), 0); err != nil {
		t.Fatalf("write signature: %v", err)
	}

	// Metadata region: header + one "File Parameters" entry + the item
	// itself (BlockSize, LeaveBlocksAllocated/HasParent flags).
	meta := make([]byte, 4096)
	copy(meta[0:8], "metadata")
	putLE16(meta, 10, 1) // EntryCount = 1
	entryOff := 32
	copy(meta[entryOff:entryOff+16], vhdxMetaFileParams)
	const itemRelOffset = 64
	putLE32(meta, entryOff+16, itemRelOffset) // Offset, relative to region start
	putLE32(meta, entryOff+20, 8)             // Length
	item := meta[itemRelOffset : itemRelOffset+8]
	putLE32(item, 0, blockSize)
	putLE32(item, 4, 0) // no HasParent bit
	if _, err := file.WriteAt(meta, metaRegionOffset); err != nil {
		t.Fatalf("write metadata region: %v", err)
	}

	// BAT: entry 0 fully present at block0FileOffset (must be a multiple
	// of 1 MiB), entry 1 not present.
	bat := make([]byte, 16)
	putLE64(bat, 0, uint64(block0FileOffset/vhdxBATFileOffsetGranularBy)<<vhdxBATFileOffsetShift|vhdxBATStateFullyPresent)
	putLE64(bat, 8, 0) // state 0 = not present
	if _, err := file.WriteAt(bat, batRegionOffset); err != nil {
		t.Fatalf("write bat: %v", err)
	}

	block0 := bytes.Repeat([]byte{0x77}, blockSize)
	if _, err := file.WriteAt(block0, block0FileOffset); err != nil {
		t.Fatalf("write block0: %v", err)
	}

	// Region table: two entries, BAT and Metadata.
	region := make([]byte, 4096)
	copy(region[0:4], "regi")
	putLE32(region, 8, 2) // EntryCount = 2
	copy(region[16:32], vhdxRegionBAT)
	putLE64(region, 32, uint64(batRegionOffset))
	putLE32(region, 40, 16)
	copy(region[48:64], vhdxRegionMetadata)
	putLE64(region, 64, uint64(metaRegionOffset))
	putLE32(region, 72, 4096)
	if _, err := file.WriteAt(region, vhdxRegionTableOffset); err != nil {
		t.Fatalf("write region table: %v", err)
	}

	return file, block0
}

func putLE16(b []byte, off int, v uint16) {
	b[off] = byte(v)
	b[off+1] = byte(v >> 8)
}

func TestVHDXLogicalDiskMapsFullyPresentBlock(t *testing.T) {
	file, block0 := buildVHDX(t)
	ld, err := newVHDXLogicalDisk(file)
	if err != nil {
		t.Fatalf("newVHDXLogicalDisk: %v", err)
	}
	got, err := ld.ReadLogical(0, int64(len(block0)))
	if err != nil {
		t.Fatalf("ReadLogical: %v", err)
	}
	if !bytes.Equal(got, block0) {
		t.Fatalf("block0 mismatch")
	}
}

func TestVHDXLogicalDiskNotPresentBlockReadsZero(t *testing.T) {
	file, block0 := buildVHDX(t)
	ld, err := newVHDXLogicalDisk(file)
	if err != nil {
		t.Fatalf("newVHDXLogicalDisk: %v", err)
	}
	got, err := ld.ReadLogical(int64(len(block0)), int64(len(block0)))
	if err != nil {
		t.Fatalf("ReadLogical: %v", err)
	}
	if !bytes.Equal(got, make([]byte, len(block0))) {
		t.Fatalf("expected zero-filled not-present block")
	}
}

func TestOpenLogicalDiskRejectsUnmappedFormat(t *testing.T) {
	file, path := createTemp(t, "cdrom.iso", 512)
	_ = file
	if _, _, err := openLogicalDisk(path, FormatISO); !errors.Is(err, ErrCannotMapLogicalDisk) {
		t.Fatalf("err = %v, want ErrCannotMapLogicalDisk", err)
	}
}

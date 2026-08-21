package vmdisk

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// partitionScanWindow is the only region of a disk's logical byte
// address space this package ever reads: enough to cover an MBR (LBA0)
// or a GPT protective MBR (LBA0) + GPT header (LBA1) + a generously
// sized partition entry array (LBA2 onward, 128 x 128-byte entries =
// 16 KiB), never the disk's actual filesystem content.
const partitionScanWindow = 64 * 1024

// maxMappedRegionSize bounds every single block/cluster/grain this
// package will read while resolving a logical offset, regardless of
// what a (possibly malformed) container header claims - a corrupt
// BlockSize/cluster_bits field can never make this package allocate or
// read an unbounded amount of memory.
const maxMappedRegionSize = 64 << 20 // 64 MiB

// ErrCannotMapLogicalDisk is returned when a container format's
// internal block-mapping cannot be safely resolved by this package - a
// compressed QCOW2 cluster at the region requested, a VMDK
// text-descriptor extent chain, or a differencing VHDX, for example.
// Callers must fail closed (require an explicit override) rather than
// guess which disk is bootable.
var ErrCannotMapLogicalDisk = errors.New("vmdisk: cannot safely resolve this disk's logical block mapping")

// logicalDisk resolves a virtual disk's logical byte address space to
// the underlying container file's actual bytes, one bounded region at a
// time.
type logicalDisk interface {
	// ReadLogical returns exactly length bytes of logical disk content
	// starting at offset. Unallocated regions read as zero, matching how
	// a real hypervisor presents a sparse disk to a guest.
	ReadLogical(offset, length int64) ([]byte, error)
}

// openLogicalDisk opens path and returns a logicalDisk for it if this
// package knows how to map format's container structure; the caller
// owns the returned closer.
func openLogicalDisk(path string, format Format) (logicalDisk, io.Closer, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("vmdisk: lstat %s: %w", path, err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("vmdisk: %s must be a regular non-symlink disk image", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("vmdisk: open %s: %w", path, err)
	}
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(pathInfo, openedInfo) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("vmdisk: %s changed while it was being opened", path)
	}
	var ld logicalDisk
	switch format {
	case FormatRAW:
		ld = &rawLogicalDisk{file: file}
	case FormatVHD:
		ld, err = newVHDLogicalDisk(file)
	case FormatVHDX:
		ld, err = newVHDXLogicalDisk(file)
	case FormatQCOW2:
		ld, err = newQCOW2LogicalDisk(file)
	case FormatVMDK:
		ld, err = newVMDKLogicalDisk(file)
	default:
		err = fmt.Errorf("%w: no logical-disk mapping for format %s", ErrCannotMapLogicalDisk, format)
	}
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return ld, file, nil
}

// readAtBounded reads exactly length bytes at offset, treating a short
// read or EOF as the remainder being zero (a sparse or short region) -
// never an error - so callers never have to special-case running off
// the end of an allocated block.
func readAtBounded(file *os.File, offset, length int64) ([]byte, error) {
	if offset < 0 || length < 0 || length > maxMappedRegionSize {
		return nil, fmt.Errorf("vmdisk: invalid bounded read offset=%d length=%d", offset, length)
	}
	buf := make([]byte, length)
	if length == 0 {
		return buf, nil
	}
	_, err := file.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("vmdisk: read: %w", err)
	}
	return buf, nil
}

type rawLogicalDisk struct{ file *os.File }

func (r *rawLogicalDisk) ReadLogical(offset, length int64) ([]byte, error) {
	return readAtBounded(r.file, offset, length)
}

func be64(b []byte) uint64 {
	var v uint64
	for _, c := range b[:8] {
		v = v<<8 | uint64(c)
	}
	return v
}
func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
func le64(b []byte) uint64 {
	var v uint64
	for i := 7; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}

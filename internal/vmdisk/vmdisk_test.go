package vmdisk

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// padTo pads content to at least size bytes with zeroes, never truncating.
func padTo(content []byte, size int) []byte {
	if len(content) >= size {
		return content
	}
	padded := make([]byte, size)
	copy(padded, content)
	return padded
}

func TestDetectQCOW2(t *testing.T) {
	header := []byte{0x51, 0x46, 0x49, 0xfb, 0x00, 0x00, 0x00, 0x03}
	path := writeTemp(t, "disk.qcow2", padTo(header, minDiskSize))
	info, err := Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.Format != FormatQCOW2 {
		t.Fatalf("format = %s, want qcow2", info.Format)
	}
}

func TestDetectQCOW2RejectsVersion1(t *testing.T) {
	header := []byte{0x51, 0x46, 0x49, 0xfb, 0x00, 0x00, 0x00, 0x01}
	path := writeTemp(t, "disk.qcow", padTo(header, minDiskSize))
	if _, err := Detect(path); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("err = %v, want ErrUnsupportedFormat for qcow v1", err)
	}
}

func TestDetectVMDKSparse(t *testing.T) {
	header := append([]byte("KDMV"), bytes.Repeat([]byte{0}, 4)...)
	path := writeTemp(t, "disk.vmdk", padTo(header, minDiskSize))
	info, err := Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.Format != FormatVMDK {
		t.Fatalf("format = %s, want vmdk", info.Format)
	}
}

func TestDetectVMDKDescriptor(t *testing.T) {
	content := []byte("# Disk DescriptorFile\nversion=1\nCID=fffffffe\n")
	path := writeTemp(t, "disk.vmdk", padTo(content, minDiskSize))
	info, err := Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.Format != FormatVMDK {
		t.Fatalf("format = %s, want vmdk", info.Format)
	}
}

func TestDetectVHDXFile(t *testing.T) {
	path := writeTemp(t, "disk.vhdx", padTo([]byte("vhdxfile"), minDiskSize))
	info, err := Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.Format != FormatVHDX {
		t.Fatalf("format = %s, want vhdx", info.Format)
	}
}

func TestDetectVHDHeaderCopy(t *testing.T) {
	path := writeTemp(t, "disk.vhd", padTo([]byte("conectix"), minDiskSize))
	info, err := Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.Format != FormatVHD {
		t.Fatalf("format = %s, want vhd", info.Format)
	}
}

func TestDetectVHDFooterOnly(t *testing.T) {
	// A fixed VHD: no header cookie at the start, only a 512-byte footer
	// at the very end - the case detectVHDHeader alone would miss.
	content := make([]byte, minDiskSize*3)
	copy(content[len(content)-footerWindow:], []byte("conectix"))
	path := writeTemp(t, "disk.vhd", content)
	info, err := Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.Format != FormatVHD {
		t.Fatalf("format = %s, want vhd", info.Format)
	}
	if info.SizeBytes != int64(len(content)) {
		t.Fatalf("SizeBytes = %d, want %d", info.SizeBytes, len(content))
	}
}

func TestDetectISO(t *testing.T) {
	content := make([]byte, 16*2048+2048)
	copy(content[16*2048+1:], []byte("CD001"))
	path := writeTemp(t, "disk.iso", content)
	info, err := Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.Format != FormatISO {
		t.Fatalf("format = %s, want iso", info.Format)
	}
}

func TestDetectRAWViaMBR(t *testing.T) {
	content := make([]byte, minDiskSize)
	content[510] = 0x55
	content[511] = 0xaa
	path := writeTemp(t, "disk.raw", content)
	info, err := Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.Format != FormatRAW {
		t.Fatalf("format = %s, want raw", info.Format)
	}
}

func TestDetectRAWViaGPT(t *testing.T) {
	content := make([]byte, 1024)
	copy(content[512:520], []byte("EFI PART"))
	path := writeTemp(t, "disk.raw", content)
	info, err := Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.Format != FormatRAW {
		t.Fatalf("format = %s, want raw", info.Format)
	}
}

func TestDetectRejectsUnrecognizedContent(t *testing.T) {
	path := writeTemp(t, "disk.bin", bytes.Repeat([]byte{0x42}, minDiskSize))
	if _, err := Detect(path); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
	}
}

func TestDetectRejectsTooSmallFile(t *testing.T) {
	path := writeTemp(t, "tiny.bin", []byte{0x51, 0x46, 0x49, 0xfb})
	if _, err := Detect(path); err == nil {
		t.Fatal("expected an error for a file smaller than any supported header")
	}
}

func TestDetectRejectsEmptyFile(t *testing.T) {
	path := writeTemp(t, "empty.bin", nil)
	if _, err := Detect(path); err == nil {
		t.Fatal("expected an error for an empty file")
	}
}

func TestDetectRejectsTruncatedQCOW2Magic(t *testing.T) {
	// Only the first 3 of the 4 magic bytes are present - must not panic
	// or misclassify, and must still be bounded (io.ReadFull tolerates
	// the short read via io.ErrUnexpectedEOF).
	content := padTo([]byte{0x51, 0x46, 0x49}, minDiskSize)
	path := writeTemp(t, "truncated.qcow2", content)
	if _, err := Detect(path); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("err = %v, want ErrUnsupportedFormat for truncated magic", err)
	}
}

func TestDetectRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	if _, err := Detect(dir); err == nil {
		t.Fatal("expected an error for a directory")
	}
}

func TestDetectRejectsMissingFile(t *testing.T) {
	if _, err := Detect(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

// TestDetectBoundsHeaderReadOnSparseFile proves Detect never reads
// beyond headerWindow bytes from the front of the file even when the
// file claims to be enormous - a sparse file here stands in for a
// maliciously (or just very large) disk image, and Detect must
// identify it (or reject it) in bounded time and memory rather than
// reading gigabytes.
func TestDetectBoundsHeaderReadOnSparseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge-sparse.qcow2")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := file.Write([]byte{0x51, 0x46, 0x49, 0xfb, 0x00, 0x00, 0x00, 0x03}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	const hugeSize = 1 << 40 // 1 TiB, sparse - not actually allocated on disk
	if err := file.Truncate(hugeSize); err != nil {
		t.Skipf("sparse truncate unsupported on this filesystem: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	info, err := Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.Format != FormatQCOW2 {
		t.Fatalf("format = %s, want qcow2", info.Format)
	}
	if info.SizeBytes != hugeSize {
		t.Fatalf("SizeBytes = %d, want %d", info.SizeBytes, hugeSize)
	}
}

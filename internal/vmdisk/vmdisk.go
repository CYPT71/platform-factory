// Package vmdisk identifies legacy virtual-machine disk image formats by
// header inspection only. It never mounts, loop-attaches, or interprets
// partition/filesystem content - see Meine-Graal v6.2 "Formats de disques"
// for the broader legacy-VM-import vision this is a first, deliberately
// narrow slice of: identification, not parsing.
package vmdisk

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

// Format is one of the disk container formats this package can identify.
type Format string

const (
	FormatRAW   Format = "raw"
	FormatQCOW2 Format = "qcow2"
	FormatVMDK  Format = "vmdk"
	FormatVHD   Format = "vhd"
	FormatVHDX  Format = "vhdx"
	FormatISO   Format = "iso"
)

// ErrUnsupportedFormat is returned when a file's header matches none of
// the formats this package recognizes. Detect fails closed: an
// unrecognized header is never guessed at or treated as RAW.
var ErrUnsupportedFormat = errors.New("vmdisk: unrecognized or unsupported disk image format")

// headerWindow bounds every read Detect performs, regardless of the
// candidate file's actual size - a maliciously large file never causes
// more than this many bytes to be read from the front of the file.
const headerWindow = 64 * 1024

// footerWindow bounds the trailing read used for VHD footer detection.
const footerWindow = 512

// minDiskSize is the smallest size Detect considers plausible for any
// supported format; anything smaller fails the size check before any
// content is inspected.
const minDiskSize = 512

// Info describes what Detect found.
type Info struct {
	Format    Format
	Path      string
	SizeBytes int64
	Evidence  string // human-readable description of the matched signature
}

// Detect identifies path's disk image format by reading only bounded
// windows at the start (and, for VHD, the end) of the file - never the
// full file, and never anything that would require mounting or
// interpreting a partition table or filesystem. It opens the file
// read-only and never writes to it.
func Detect(path string) (Info, error) {
	file, err := os.Open(path)
	if err != nil {
		return Info{}, fmt.Errorf("vmdisk: open %s: %w", path, err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return Info{}, fmt.Errorf("vmdisk: stat %s: %w", path, err)
	}
	if stat.IsDir() {
		return Info{}, fmt.Errorf("vmdisk: %s is a directory, not a disk image", path)
	}
	size := stat.Size()
	if size < minDiskSize {
		return Info{}, fmt.Errorf("vmdisk: %s is %d bytes, smaller than the %d-byte minimum for any supported format", path, size, minDiskSize)
	}

	head := make([]byte, headerWindow)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return Info{}, fmt.Errorf("vmdisk: read header of %s: %w", path, err)
	}
	head = head[:n]

	if info, ok := detectVHDX(head); ok {
		return finish(info, path, size), nil
	}
	if info, ok := detectQCOW2(head); ok {
		return finish(info, path, size), nil
	}
	if info, ok := detectVMDK(head); ok {
		return finish(info, path, size), nil
	}
	if info, ok := detectISO(head); ok {
		return finish(info, path, size), nil
	}
	if info, ok := detectVHDHeader(head); ok {
		return finish(info, path, size), nil
	}
	if info, ok, err := detectVHDFooter(file, size); err != nil {
		return Info{}, err
	} else if ok {
		return finish(info, path, size), nil
	}
	if info, ok := detectRAW(head); ok {
		return finish(info, path, size), nil
	}

	return Info{}, fmt.Errorf("%w: %s", ErrUnsupportedFormat, path)
}

func finish(info Info, path string, size int64) Info {
	info.Path = path
	info.SizeBytes = size
	return info
}

// detectQCOW2 checks the "QFI\xfb" magic and requires version 2 or 3 -
// version 1 (the old, pre-QCOW2 format) is deliberately not accepted as
// qcow2, since claiming support for a format this package cannot
// actually distinguish from a real QCOW2 image would be dishonest.
func detectQCOW2(head []byte) (Info, bool) {
	magic := []byte{0x51, 0x46, 0x49, 0xfb}
	if len(head) < 8 || !bytes.Equal(head[:4], magic) {
		return Info{}, false
	}
	version := be32(head[4:8])
	if version != 2 && version != 3 {
		return Info{}, false
	}
	return Info{Format: FormatQCOW2, Evidence: fmt.Sprintf("QFI\\xfb magic, version %d", version)}, true
}

// detectVMDK recognizes both the binary sparse-extent header ("KDMV")
// and the plain-text descriptor form VMware also uses.
func detectVMDK(head []byte) (Info, bool) {
	if len(head) >= 4 && bytes.Equal(head[:4], []byte("KDMV")) {
		return Info{Format: FormatVMDK, Evidence: "KDMV sparse-extent magic"}, true
	}
	if bytes.HasPrefix(head, []byte("# Disk DescriptorFile")) {
		return Info{Format: FormatVMDK, Evidence: "text descriptor header"}, true
	}
	return Info{}, false
}

// detectVHDX checks the 8-byte "vhdxfile" file type identifier at
// offset 0.
func detectVHDX(head []byte) (Info, bool) {
	if len(head) >= 8 && bytes.Equal(head[:8], []byte("vhdxfile")) {
		return Info{Format: FormatVHDX, Evidence: "vhdxfile identifier"}, true
	}
	return Info{}, false
}

// detectVHDHeader covers dynamic/differencing VHDs, which carry a copy
// of the footer's "conectix" cookie at the start of the file as well as
// at the end.
func detectVHDHeader(head []byte) (Info, bool) {
	if len(head) >= 8 && bytes.Equal(head[:8], []byte("conectix")) {
		return Info{Format: FormatVHD, Evidence: "conectix cookie at file start"}, true
	}
	return Info{}, false
}

// detectVHDFooter covers fixed VHDs, whose only "conectix" cookie is a
// 512-byte footer at the very end of the file. Bounded to the last
// footerWindow bytes regardless of file size.
func detectVHDFooter(file *os.File, size int64) (Info, bool, error) {
	if size < footerWindow {
		return Info{}, false, nil
	}
	footer := make([]byte, footerWindow)
	if _, err := file.ReadAt(footer, size-footerWindow); err != nil && !errors.Is(err, io.EOF) {
		return Info{}, false, fmt.Errorf("vmdisk: read footer: %w", err)
	}
	if bytes.Equal(footer[:8], []byte("conectix")) {
		return Info{Format: FormatVHD, Evidence: "conectix cookie at file end (footer)"}, true, nil
	}
	return Info{}, false, nil
}

// detectISO checks the ISO 9660 Volume Descriptor identifier "CD001" at
// its fixed location, sector 16 (2048-byte sectors), one byte in.
func detectISO(head []byte) (Info, bool) {
	const offset = 16*2048 + 1
	if len(head) < offset+5 {
		return Info{}, false
	}
	if bytes.Equal(head[offset:offset+5], []byte("CD001")) {
		return Info{Format: FormatISO, Evidence: "CD001 volume descriptor at sector 16"}, true
	}
	return Info{}, false
}

// detectRAW is the fallback for files carrying none of the container
// formats' own magic: it only classifies a file as RAW when there is
// positive evidence of a partition table (MBR boot signature or a GPT
// header), never merely because nothing else matched - an
// unrecognized file is ErrUnsupportedFormat, not assumed to be RAW.
func detectRAW(head []byte) (Info, bool) {
	if len(head) >= 512 && head[510] == 0x55 && head[511] == 0xaa {
		return Info{Format: FormatRAW, Evidence: "MBR boot signature 0x55AA at offset 510"}, true
	}
	if len(head) >= 512+8 && bytes.Equal(head[512:520], []byte("EFI PART")) {
		return Info{Format: FormatRAW, Evidence: "GPT header at LBA 1"}, true
	}
	return Info{}, false
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

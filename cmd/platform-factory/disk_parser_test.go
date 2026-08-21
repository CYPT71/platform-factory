package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/layout"
	"github.com/CYPT71/platform-factory/internal/vmdisk"
)

func TestDiskParserWorkerRejectsUnknownFields(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDiskParserWorker(strings.NewReader(`{"disk_images":["disk.raw"],"command":"sh"}`), &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "unknown field") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestInspectLegacyDiskUsesIsolatedRealCLIProcess(t *testing.T) {
	disk := writeCLIExtFixture(t)
	binary := filepath.Join(t.TempDir(), "pf")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build pf: %v\n%s", err, output)
	}
	reportDir := filepath.Join(t.TempDir(), "reports")
	command := exec.Command(binary, "microvm", "inspect-legacy-disk", "--disk="+disk, "--report-dir="+reportDir)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("inspect through isolated parser: %v\n%s", err, output)
	}
	encoded, err := os.ReadFile(filepath.Join(reportDir, "discovery.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report vmdisk.DiscoveryReport
	if err := json.Unmarshal(encoded, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Disks) != 1 || report.Disks[0].Path != disk || report.Disks[0].SHA256 == "" {
		t.Fatalf("report=%+v", report)
	}
	extracted := filepath.Join(t.TempDir(), "extracted")
	command = exec.Command(binary, "microvm", "extract-legacy-app", "--disk="+disk, "--volume-index=0", "--include=/hello.txt", "--output="+extracted)
	if output, err := command.CombinedOutput(); err == nil || !strings.Contains(string(output), "incomplete extraction requires explicit approval") {
		t.Fatalf("undeclared incomplete extraction: err=%v\n%s", err, output)
	}
	if _, err := os.Stat(extracted); !os.IsNotExist(err) {
		t.Fatalf("refused extraction wrote output: %v", err)
	}
	command = exec.Command(binary, "microvm", "extract-legacy-app", "--disk="+disk, "--volume-index=0", "--include=/hello.txt", "--output="+extracted, "--allow-incomplete")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("approved isolated extraction: %v\n%s", err, output)
	}
	if content, err := os.ReadFile(filepath.Join(extracted, "hello.txt")); err != nil || string(content) != "hello" {
		t.Fatalf("extracted content=%q err=%v", content, err)
	}
	legacyLayout := filepath.Join(t.TempDir(), "legacy-oci")
	command = exec.Command(binary, "microvm", "build-legacy-oci", "--disk="+disk, "--volume-index=0", "--include=/service", "--entrypoint=/service", "--output="+legacyLayout, "--arch="+runtime.GOARCH)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build isolated legacy OCI: %v\n%s", err, output)
	}
	if _, err := layout.Verify(legacyLayout); err != nil {
		t.Fatalf("verify legacy OCI: %v", err)
	}
}

func writeCLIExtFixture(t *testing.T) string {
	t.Helper()
	const volumeStart, blockSize, blocks = 512, 1024, 128
	disk := make([]byte, volumeStart+blockSize*blocks)
	disk[446], disk[450], disk[510], disk[511] = 0x80, 0x83, 0x55, 0xaa
	binary.LittleEndian.PutUint32(disk[454:458], 1)
	binary.LittleEndian.PutUint32(disk[458:462], uint32(blockSize*blocks/512))
	volume := disk[volumeStart:]
	sb := volume[1024:2048]
	binary.LittleEndian.PutUint32(sb[0:4], 16)
	binary.LittleEndian.PutUint32(sb[4:8], blocks)
	binary.LittleEndian.PutUint32(sb[20:24], 1)
	binary.LittleEndian.PutUint32(sb[32:36], blocks)
	binary.LittleEndian.PutUint32(sb[40:44], 16)
	binary.LittleEndian.PutUint16(sb[56:58], 0xef53)
	binary.LittleEndian.PutUint32(sb[76:80], 1)
	binary.LittleEndian.PutUint16(sb[88:90], 128)
	binary.LittleEndian.PutUint32(volume[2*blockSize+8:2*blockSize+12], 5)
	inodes := volume[5*blockSize:]
	root := inodes[128:256]
	binary.LittleEndian.PutUint16(root[0:2], 0x41ed)
	binary.LittleEndian.PutUint32(root[4:8], blockSize)
	binary.LittleEndian.PutUint16(root[26:28], 2)
	binary.LittleEndian.PutUint32(root[40:44], 10)
	file := inodes[11*128 : 12*128]
	binary.LittleEndian.PutUint16(file[0:2], 0x81a0)
	binary.LittleEndian.PutUint16(file[2:4], 1000)
	binary.LittleEndian.PutUint32(file[4:8], 5)
	binary.LittleEndian.PutUint16(file[24:26], 1001)
	binary.LittleEndian.PutUint16(file[26:28], 1)
	binary.LittleEndian.PutUint32(file[40:44], 11)
	binary.LittleEndian.PutUint32(file[104:108], 12) // xattr: incomplete unless approved
	service := inodes[12*128 : 13*128]
	binary.LittleEndian.PutUint16(service[0:2], 0x81e8) // regular 0750
	binary.LittleEndian.PutUint16(service[2:4], 1000)
	binary.LittleEndian.PutUint32(service[4:8], 64)
	binary.LittleEndian.PutUint16(service[24:26], 1001)
	binary.LittleEndian.PutUint16(service[26:28], 1)
	binary.LittleEndian.PutUint32(service[40:44], 12)
	directory := volume[10*blockSize : 11*blockSize]
	writeCLIDirEntry(directory[0:12], 2, 12, ".", 2)
	writeCLIDirEntry(directory[12:24], 2, 12, "..", 2)
	writeCLIDirEntry(directory[24:44], 12, 20, "hello.txt", 1)
	writeCLIDirEntry(directory[44:], 13, blockSize-44, "service", 1)
	copy(volume[11*blockSize:], "hello")
	copy(volume[12*blockSize:], minimalCLIStaticELF())
	filename := filepath.Join(t.TempDir(), "os.raw")
	if err := os.WriteFile(filename, disk, 0o600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func minimalCLIStaticELF() []byte {
	data := make([]byte, 64)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(data[16:18], 2)
	machine := uint16(62)
	if runtime.GOARCH == "arm64" {
		machine = 183
	}
	binary.LittleEndian.PutUint16(data[18:20], machine)
	binary.LittleEndian.PutUint32(data[20:24], 1)
	binary.LittleEndian.PutUint16(data[52:54], 64)
	binary.LittleEndian.PutUint16(data[54:56], 56)
	binary.LittleEndian.PutUint16(data[58:60], 64)
	return data
}

func writeCLIDirEntry(target []byte, inode uint32, length int, name string, kind byte) {
	binary.LittleEndian.PutUint32(target[0:4], inode)
	binary.LittleEndian.PutUint16(target[4:6], uint16(length))
	target[6], target[7] = byte(len(name)), kind
	copy(target[8:], name)
}

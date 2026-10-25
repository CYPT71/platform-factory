package main

import (
	"bytes"
	"context"
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

// Parser boundary tests exercise the disposable worker directly as well as
// refusing a lying/malformed worker response before any result is trusted.
func TestDiskParserWorkerSuccessfulDiscoveryExtractionAndStrictInput(t *testing.T) {
	disk := writeCLIExtFixture(t)
	for _, request := range []diskParserRequest{
		{Operation: "inspect", DiskImages: []string{disk}},
		{Operation: "extract", DiskImages: []string{disk}, VolumeIndex: 0, SelectedPaths: []string{"/hello.txt"}, Output: filepath.Join(t.TempDir(), "export"), AllowIncomplete: true},
	} {
		input, _ := json.Marshal(request)
		var out, diagnostics bytes.Buffer
		if code := runDiskParserWorker(bytes.NewReader(input), &out, &diagnostics); code != 0 {
			t.Fatalf("worker=%d %s", code, &diagnostics)
		}
		if !json.Valid(out.Bytes()) {
			t.Fatalf("non-JSON worker result: %s", &out)
		}
	}
	for _, raw := range []string{`{}`, `{"disk_images":["disk.raw"]} {}`, `{"disk_images":["disk.raw"],"operation":"exec"}`, `{"disk_images":["missing.raw"]}`, `{"disk_images":["missing.raw"],"operation":"extract","selected_paths":["/hello"],"output":"unused"}`} {
		var out, diagnostics bytes.Buffer
		if code := runDiskParserWorker(strings.NewReader(raw), &out, &diagnostics); code == 0 || out.Len() != 0 {
			t.Fatalf("accepted invalid request %q", raw)
		}
	}
}
func TestDiskParserRequestBoundsAndBoundedOutput(t *testing.T) {
	for _, request := range []diskParserRequest{
		{}, {DiskImages: make([]string, maxDiskParserDisks+1)},
		{DiskImages: []string{" "}}, {DiskImages: []string{strings.Repeat("x", maxDiskPathLength+1)}},
		{DiskImages: []string{"disk\x00.raw"}}, {DiskImages: []string{"disk"}, BootDiskOverride: "bad\x00path"},
		{DiskImages: []string{"disk"}, Operation: "extract"},
	} {
		if err := validateDiskParserRequest(request); err == nil {
			t.Fatalf("accepted invalid request %+v", request)
		}
	}
	var buffer boundedBuffer
	buffer.limit = 4
	if n, err := buffer.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	if n, err := buffer.Write([]byte("de")); err == nil || n != 1 || buffer.String() != "abcd" {
		t.Fatal(n, err, buffer.String())
	}
	if n, err := buffer.Write([]byte("x")); err == nil || n != 0 {
		t.Fatal("output budget bypassed")
	}
}
func TestIsolatedDiskParserRejectsChangedIdentityMalformedAndTrailingResults(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix fixture launcher")
	}
	root := t.TempDir()
	disk := filepath.Join(root, "disk.raw")
	cases := []struct {
		payload string
		valid   bool
	}{
		{`{"disks":[{"path":"` + filepath.ToSlash(disk) + `"}]}`, true},
		{`{"disks":[{"path":"changed"}]}`, false},
		{`{"disks":[]}`, false}, {`{"unexpected":true}`, false}, {`not-json`, false}, {`{"disks":[]} {}`, false},
	}
	for _, test := range cases {
		launcher := filepath.Join(t.TempDir(), "worker")
		script := "#!/bin/sh\n/bin/cat >/dev/null\nprintf '%s' '" + strings.ReplaceAll(test.payload, "'", "'\\''") + "'\n"
		if err := os.WriteFile(launcher, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		_, err := parseLegacyDisksIsolated(context.Background(), launcher, []string{disk}, "")
		if (err == nil) != test.valid {
			t.Fatalf("payload %s: %v", test.payload, err)
		}
	}
	if _, err := parseLegacyDisksIsolated(context.Background(), "missing", []string{disk}, ""); err == nil {
		t.Fatal("missing worker accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := parseLegacyDisksIsolated(ctx, "/bin/sleep", []string{disk}, ""); err == nil {
		t.Fatal("cancelled worker accepted")
	}
}
func TestIsolatedExtractionRejectsChangedIdentityAndAcceptsPinnedResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix fixture launcher")
	}
	request := diskParserRequest{DiskImages: []string{"disk.raw"}, Output: "output", VolumeIndex: 2, SelectedPaths: []string{"/service"}}
	for _, test := range []struct {
		payload string
		valid   bool
	}{
		{`{"disk_path":"disk.raw","output":"output","volume_index":2}`, true},
		{`{"disk_path":"other","output":"output","volume_index":2}`, false},
		{`{"disk_path":"disk.raw","output":"other","volume_index":2}`, false},
		{`{}`, false}, {`[]`, false}, {`{} {}`, false},
	} {
		launcher := filepath.Join(t.TempDir(), "worker")
		script := "#!/bin/sh\n/bin/cat >/dev/null\nprintf '%s' '" + test.payload + "'\n"
		if err := os.WriteFile(launcher, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		_, err := extractLegacyFilesIsolated(context.Background(), launcher, request)
		if (err == nil) != test.valid {
			t.Fatalf("payload %s: %v", test.payload, err)
		}
	}
}

func TestLegacyExtractionAndOCIBuildThroughApplicationCLI(t *testing.T) {
	disk := writeCLIExtFixture(t)
	var out, diagnostics bytes.Buffer
	output := filepath.Join(t.TempDir(), "extracted")
	args := []string{"extract-legacy-app", "--disk", disk, "--include", "/hello.txt", "--output", output}
	if code := runMicroVM(args, &out, &diagnostics, nil); code != 1 {
		t.Fatalf("incomplete extraction accepted: %d %s", code, &diagnostics)
	}
	out.Reset()
	diagnostics.Reset()
	if code := runMicroVM(append(args, "--allow-incomplete"), &out, &diagnostics, nil); code != 0 {
		t.Fatalf("approved extraction: %d %s", code, &diagnostics)
	}
	content, err := os.ReadFile(filepath.Join(output, "hello.txt"))
	if err != nil || string(content) != "hello" {
		t.Fatalf("incorrect extracted file %q: %v", content, err)
	}
	out.Reset()
	diagnostics.Reset()
	ociOutput := filepath.Join(t.TempDir(), "oci")
	args = []string{"build-legacy-oci", "--disk", disk, "--include", "/service", "--entrypoint", "/service", "--output", ociOutput, "--arch", runtime.GOARCH}
	if code := runMicroVM(args, &out, &diagnostics, nil); code != 0 {
		t.Fatalf("OCI build: %d %s", code, &diagnostics)
	}
	if _, err := layout.Verify(ociOutput); err != nil {
		t.Fatal(err)
	}
	// Refuse entrypoint substitution and invalid identity overrides.
	args = []string{"build-legacy-oci", "--disk", disk, "--include", "/service", "--entrypoint", "/other", "--output", filepath.Join(t.TempDir(), "oci")}
	if code := runMicroVM(args, &out, &diagnostics, nil); code != 2 {
		t.Fatal("unselected entrypoint accepted")
	}
	args = []string{"build-legacy-oci", "--disk", disk, "--include", "/service", "--entrypoint", "/service", "--output", filepath.Join(t.TempDir(), "oci"), "--arch", runtime.GOARCH, "--user", "0"}
	if code := runMicroVM(args, &out, &diagnostics, nil); code != 1 {
		t.Fatal("root user override accepted")
	}
}

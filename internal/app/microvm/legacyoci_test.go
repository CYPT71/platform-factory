package microvm

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/CYPT71/platform-factory/internal/layout"
	"github.com/CYPT71/platform-factory/internal/vmdisk"
)

func TestBuildLegacyOCIPreservesReviewedRuntimeContract(t *testing.T) {
	root := t.TempDir()
	serviceDir := filepath.Join(root, "opt", "app")
	if err := os.MkdirAll(serviceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := minimalStaticELF(t)
	service := filepath.Join(serviceDir, "service")
	if err := os.WriteFile(service, payload, 0o750); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(serviceDir, "config.yaml")
	if err := os.WriteFile(config, []byte("mode: demo\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	report := vmdisk.ExtractionReport{Complete: true, Extracted: []vmdisk.ExtractedFile{{Path: "/opt/app/service", Mode: 0o750, UID: 2000, GID: 2001, Size: uint64(len(payload))}, {Path: "/opt/app/config.yaml", Mode: 0o640, UID: 1000, GID: 1001, Size: 11}}, System: &vmdisk.SystemInventory{Mounts: []vmdisk.SystemMount{{MountPoint: "/srv/data"}}, ProbablePorts: []int{8443}, Users: []vmdisk.SystemUser{{Name: "app", UID: 1000, GID: 1001}}, Groups: []vmdisk.SystemGroup{{Name: "app", GID: 1001}}, ServiceConfigurations: []vmdisk.ServiceConfiguration{{Source: "/etc/systemd/system/demo.service", Entrypoint: "/opt/app/service", Args: []string{"--serve"}, Environment: map[string]string{"MODE": "production"}, WorkingDirectory: "/srv/app", User: "app", Group: "app"}}}}
	output := filepath.Join(t.TempDir(), "oci")
	result, err := BuildLegacyOCI(LegacyOCIOptions{ExtractionRoot: root, Report: report, Entrypoint: "/opt/app/service", Output: output, Architecture: runtime.GOARCH, Image: "legacy-app", Tag: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Digest == "" || result.User != "1000:1001" || result.WorkingDir != "/srv/app" || result.Profile != "static" {
		t.Fatalf("result=%+v", result)
	}
	if len(result.Volumes) != 1 || result.Volumes[0] != "/srv/data" || len(result.Ports) != 1 || result.Ports[0] != "8443/tcp" {
		t.Fatalf("runtime metadata=%+v", result)
	}
	if len(result.Args) != 1 || result.Args[0] != "--serve" || result.Environment["MODE"] != "production" {
		t.Fatalf("service runtime conversion=%+v", result)
	}
	if _, err := layout.Verify(output); err != nil {
		t.Fatalf("verify OCI layout: %v", err)
	}
	secondOutput := filepath.Join(t.TempDir(), "oci")
	second, err := BuildLegacyOCI(LegacyOCIOptions{ExtractionRoot: root, Report: report, Entrypoint: "/opt/app/service", Output: secondOutput, Architecture: runtime.GOARCH, Image: "legacy-app", Tag: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Digest != result.Digest {
		t.Fatalf("legacy OCI build is not reproducible: %s != %s", result.Digest, second.Digest)
	}
	assertIdenticalLayout(t, output, secondOutput)
}

func assertIdenticalLayout(t *testing.T, first, second string) {
	t.Helper()
	err := filepath.Walk(first, func(filename string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(first, filename)
		if err != nil {
			return err
		}
		left, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		right, err := os.ReadFile(filepath.Join(second, relative))
		if err != nil {
			return err
		}
		if !bytes.Equal(left, right) {
			t.Fatalf("OCI layout file %s differs between builds", relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func minimalStaticELF(t *testing.T) []byte {
	t.Helper()
	data := make([]byte, 64)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1}) // ELF64, little endian
	binary.LittleEndian.PutUint16(data[16:18], 2)    // ET_EXEC
	machine := uint16(62)                            // EM_X86_64
	if runtime.GOARCH == "arm64" {
		machine = 183 // EM_AARCH64
	}
	binary.LittleEndian.PutUint16(data[18:20], machine)
	binary.LittleEndian.PutUint32(data[20:24], 1)
	binary.LittleEndian.PutUint16(data[52:54], 64)
	binary.LittleEndian.PutUint16(data[54:56], 56)
	binary.LittleEndian.PutUint16(data[58:60], 64)
	return data
}

func TestBuildLegacyOCIRefusesUnreviewedOrRootRuntime(t *testing.T) {
	root := t.TempDir()
	if _, err := BuildLegacyOCI(LegacyOCIOptions{ExtractionRoot: root, Report: vmdisk.ExtractionReport{Complete: false}, Entrypoint: "/app/service"}); err == nil {
		t.Fatal("unreviewed incomplete extraction accepted")
	}
}

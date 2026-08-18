package oci

import (
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestELFMachine(t *testing.T) {
	if elfMachine("amd64") != elf.EM_X86_64 || elfMachine("arm64") != elf.EM_AARCH64 || elfMachine("unknown") != 0 {
		t.Fatal("unexpected architecture mapping")
	}
}

func TestInspectSyntheticDynamicELF(t *testing.T) {
	filename := t.TempDir() + "/dynamic"
	interpreter := "/lib64/ld-linux-x86-64.so.2"
	data := make([]byte, 64+56+len(interpreter)+1)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	order := binary.LittleEndian
	order.PutUint16(data[16:], uint16(elf.ET_EXEC))
	order.PutUint16(data[18:], uint16(elf.EM_X86_64))
	order.PutUint32(data[20:], 1)
	order.PutUint64(data[32:], 64)
	order.PutUint16(data[52:], 64)
	order.PutUint16(data[54:], 56)
	order.PutUint16(data[56:], 1)
	order.PutUint16(data[58:], 64)
	order.PutUint32(data[64:], uint32(elf.PT_INTERP))
	order.PutUint32(data[68:], uint32(elf.PF_R))
	order.PutUint64(data[72:], 120)
	order.PutUint64(data[96:], uint64(len(interpreter)+1))
	order.PutUint64(data[104:], uint64(len(interpreter)+1))
	order.PutUint64(data[112:], 1)
	copy(data[120:], interpreter)
	if err := os.WriteFile(filename, data, 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := inspectELF(filename)
	if err != nil {
		t.Fatal(err)
	}
	if info.interpreter != interpreter || info.machine != elf.EM_X86_64 {
		t.Fatalf("ELF info = %+v", info)
	}
	if err := validateELFClosure(filename, "amd64", "static", nil); err == nil || !strings.Contains(err.Error(), "static profile") {
		t.Fatalf("static profile error = %v", err)
	}
	if err := validateELFClosure(filename, "amd64", "musl", nil); err == nil || !strings.Contains(err.Error(), "musl profile") {
		t.Fatalf("musl profile error = %v", err)
	}
	if err := validateELFClosure(filename, "amd64", "glibc", nil); err == nil || !strings.Contains(err.Error(), "dynamic linker") {
		t.Fatalf("missing linker error = %v", err)
	}
	if err := validateELFClosure(filename, "arm64", "glibc", nil); err == nil || !strings.Contains(err.Error(), "architecture") {
		t.Fatalf("architecture error = %v", err)
	}
}

func TestValidateELFClosureSurfacesInspectFailure(t *testing.T) {
	if err := validateELFClosure(filepath.Join(t.TempDir(), "missing"), "amd64", "static", nil); err == nil {
		t.Fatal("missing binary accepted")
	}
}

func TestInspectNonELFAndMissingFile(t *testing.T) {
	file := t.TempDir() + "/plain"
	if err := os.WriteFile(file, []byte("not an ELF"), 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := inspectELF(file)
	if err != nil || info != nil {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	if _, err := inspectELF(file + "-missing"); err == nil {
		t.Fatal("missing ELF accepted")
	}
}

func TestInspectHostELFAndRejectMissingClosure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("ELF runtime test requires Linux")
	}
	info, err := inspectELF("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || info.machine == 0 {
		t.Fatalf("ELF info = %+v", info)
	}
	architecture := runtime.GOARCH
	err = validateELFClosure("/bin/sh", architecture, "glibc", nil)
	if info.interpreter != "" && (err == nil || !strings.Contains(err.Error(), "dynamic linker")) {
		t.Fatalf("missing linker error = %v", err)
	}
}

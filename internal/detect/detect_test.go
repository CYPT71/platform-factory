package detect

import (
	"archive/zip"
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectDynamicELF(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "service")
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
	result, err := Path(filename)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != "elf" || result.Profile != "glibc" ||
		result.Architecture != "amd64" || result.Interpreter != interpreter {
		t.Fatalf("result = %+v", result)
	}
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectScripts(t *testing.T) {
	for _, test := range []struct {
		shebang, kind, profile string
	}{
		{"#!/usr/bin/env python3\n", "python", "python"},
		{"#!/usr/bin/env node\n", "node", "node"},
		{"#!/usr/bin/env ruby\n", "ruby", "ruby"},
		{"#!/usr/bin/env php\n", "php", "php"},
		{"#!/bin/sh\n", "script", "unknown"},
	} {
		result, err := Path(writeFile(t, "app", test.shebang))
		if err != nil {
			t.Fatal(err)
		}
		if result.Kind != test.kind || result.Profile != test.profile {
			t.Fatalf("result = %+v", result)
		}
	}
}

func TestDetectDirectoryAndRequireAmbiguity(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"package-lock.json", "requirements.lock"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Path(root)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ambiguous || strings.Join(result.Candidates, ",") != "node,python" {
		t.Fatalf("result = %+v", result)
	}
	data, err := JSON(result)
	if err != nil || !strings.Contains(string(data), `"ambiguous": true`) {
		t.Fatalf("json=%s err=%v", data, err)
	}
}

func TestDetectCompiledAndInterpretedEcosystemMarkers(t *testing.T) {
	for _, test := range []struct {
		marker, kind, profile string
	}{
		{"go.mod", "go", "static"},
		{"Cargo.toml", "rust", "static"},
		{"Cargo.lock", "rust", "static"},
		{"Gemfile", "ruby", "ruby"},
		{"composer.json", "php", "php"},
	} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, test.marker), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := Path(root)
		if err != nil {
			t.Fatal(err)
		}
		if result.Kind != test.kind || result.Profile != test.profile || result.Ambiguous {
			t.Fatalf("marker %s: result=%+v", test.marker, result)
		}
	}
	root := t.TempDir()
	for _, name := range []string{"go.mod", "package-lock.json"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Path(root)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ambiguous || strings.Join(result.Candidates, ",") != "go,node" {
		t.Fatalf("result=%+v", result)
	}
}

func TestDetectJavaDotnetAndUnknown(t *testing.T) {
	jar := filepath.Join(t.TempDir(), "app.jar")
	file, err := os.Create(jar)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("META-INF/MANIFEST.MF")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("Manifest-Version: 1.0\n"))
	_ = writer.Close()
	_ = file.Close()
	for _, test := range []struct{ path, kind string }{
		{jar, "java"},
		{writeFile(t, "app.dll", "MZ"), "dotnet"},
		{writeFile(t, "blob", "unknown"), "unknown"},
	} {
		result, err := Path(test.path)
		if err != nil || result.Kind != test.kind {
			t.Fatalf("path=%s result=%+v err=%v", test.path, result, err)
		}
	}
}

func TestDetectErrorsAndEmptyDirectory(t *testing.T) {
	if _, err := Path(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing input accepted")
	}
	result, err := Path(t.TempDir())
	if err != nil || result.Kind != "unknown" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

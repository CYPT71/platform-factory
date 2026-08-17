package detect

import (
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
	for _, shebang := range []string{"#!/usr/bin/env python3\n", "#!/usr/bin/env node\n", "#!/usr/bin/env ruby\n", "#!/usr/bin/env php\n", "#!/bin/sh\n"} {
		result, err := Path(writeFile(t, "app", shebang))
		if err != nil {
			t.Fatal(err)
		}
		if result.Kind != "script" || result.Profile != "unknown" {
			t.Fatalf("result = %+v", result)
		}
	}
}

func TestDirectoryLanguageDetectionIsDelegatedToPlugins(t *testing.T) {
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
	if result.Kind != "unknown" || result.Profile != "unknown" || result.Ambiguous || len(result.Candidates) != 0 {
		t.Fatalf("result = %+v", result)
	}
	data, err := JSON(result)
	if err != nil || !strings.Contains(string(data), `delegated to language plugins`) {
		t.Fatalf("json=%s err=%v", data, err)
	}
}

func TestDetectDoesNotOwnEcosystemMarkers(t *testing.T) {
	for _, marker := range []string{"go.mod", "Cargo.toml", "Cargo.lock", "Gemfile", "composer.json"} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, marker), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := Path(root)
		if err != nil {
			t.Fatal(err)
		}
		if result.Kind != "unknown" || result.Profile != "unknown" || result.Ambiguous {
			t.Fatalf("marker %s: result=%+v", marker, result)
		}
	}
}

func TestDetectUnknownRegularFilesWithoutLanguagePlugins(t *testing.T) {
	for _, name := range []string{"app.jar", "app.dll", "blob"} {
		path := writeFile(t, name, "unknown")
		result, err := Path(path)
		if err != nil || result.Kind != "unknown" {
			t.Fatalf("path=%s result=%+v err=%v", path, result, err)
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

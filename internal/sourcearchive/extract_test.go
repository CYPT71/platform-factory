package sourcearchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

type testEntry struct {
	name string
	kind byte
	data string
}

func makeArchive(t *testing.T, compressed bool, entries ...testEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	var target = interface{ Write([]byte) (int, error) }(&output)
	var gz *gzip.Writer
	if compressed {
		gz = gzip.NewWriter(&output)
		target = gz
	}
	tw := tar.NewWriter(target)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Typeflag: entry.kind, Mode: 0o755, Size: int64(len(entry.data))}
		if entry.kind != tar.TypeReg {
			header.Size = 0
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if entry.kind == tar.TypeReg {
			_, _ = tw.Write([]byte(entry.data))
		}
	}
	_ = tw.Close()
	if gz != nil {
		_ = gz.Close()
	}
	return output.Bytes()
}

func TestExtractTarAndTarGzip(t *testing.T) {
	for _, format := range []string{"tar", "tar.gz"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			data := makeArchive(t, format == "tar.gz", testEntry{name: "project/", kind: tar.TypeDir}, testEntry{name: "project/app.py", kind: tar.TypeReg, data: "print('ok')\n"})
			if err := os.WriteFile(source, data, 0o600); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(root, "out")
			if err := Extract(source, destination, format); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(destination, "project", "app.py"))
			if err != nil || string(got) != "print('ok')\n" {
				t.Fatalf("content=%q err=%v", got, err)
			}
		})
	}
}

func TestExtractRejectsHostileArchiveAndCleansDestination(t *testing.T) {
	for name, entries := range map[string][]testEntry{
		"traversal": {{name: "../outside", kind: tar.TypeReg, data: "x"}},
		"absolute":  {{name: "/outside", kind: tar.TypeReg, data: "x"}},
		"symlink":   {{name: "link", kind: tar.TypeSymlink}},
		"duplicate": {{name: "same", kind: tar.TypeReg, data: "a"}, {name: "same", kind: tar.TypeReg, data: "b"}},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source.tar")
			_ = os.WriteFile(source, makeArchive(t, false, entries...), 0o600)
			destination := filepath.Join(root, "out")
			if err := Extract(source, destination, "tar"); err == nil {
				t.Fatal("hostile archive accepted")
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("partial destination remains: %v", err)
			}
		})
	}
}

func TestExtractRefusesExistingDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.tar")
	_ = os.WriteFile(source, makeArchive(t, false, testEntry{name: "app.py", kind: tar.TypeReg, data: "x"}), 0o600)
	destination := filepath.Join(root, "out")
	_ = os.Mkdir(destination, 0o700)
	_ = os.WriteFile(filepath.Join(destination, "owned"), []byte("keep"), 0o600)
	if err := Extract(source, destination, "tar"); err == nil {
		t.Fatal("existing destination accepted")
	}
	if got, err := os.ReadFile(filepath.Join(destination, "owned")); err != nil || string(got) != "keep" {
		t.Fatalf("existing destination changed: %q %v", got, err)
	}
}

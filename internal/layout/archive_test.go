package layout

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func archiveBytes(t *testing.T, entries map[string]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for name, kind := range entries {
		h := &tar.Header{Name: name, Mode: 0o600, Typeflag: kind}
		if kind == tar.TypeReg {
			h.Size = 1
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			_, _ = tw.Write([]byte("x"))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return b.Bytes()
}
func TestVerifyArchiveRejectsHostileEntries(t *testing.T) {
	for name, entries := range map[string]map[string]byte{"traversal": {"../x": tar.TypeReg}, "absolute": {"/x": tar.TypeReg}, "symlink": {"x": tar.TypeSymlink}, "hardlink": {"x": tar.TypeLink}, "duplicate": nil} {
		t.Run(name, func(t *testing.T) {
			var data []byte
			if name == "duplicate" {
				var b bytes.Buffer
				gz := gzip.NewWriter(&b)
				tw := tar.NewWriter(gz)
				for range 2 {
					_ = tw.WriteHeader(&tar.Header{Name: "x", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg})
					_, _ = tw.Write([]byte("x"))
				}
				_ = tw.Close()
				_ = gz.Close()
				data = b.Bytes()
			} else {
				data = archiveBytes(t, entries)
			}
			if err := VerifyArchive(context.Background(), "oci-layout.tar.gz", bytes.NewReader(data)); err == nil {
				t.Fatal("hostile archive accepted")
			}
		})
	}
}
func TestVerifyArchiveRejectsConcatenatedAndTrailingGzip(t *testing.T) {
	base := archiveBytes(t, map[string]byte{"x": tar.TypeReg})
	for name, data := range map[string][]byte{"concatenated": append(append([]byte(nil), base...), base...), "trailing": append(append([]byte(nil), base...), []byte("junk")...)} {
		t.Run(name, func(t *testing.T) {
			if err := VerifyArchive(context.Background(), "oci-layout.tar.gz", bytes.NewReader(data)); err == nil {
				t.Fatal("trailing stream accepted")
			}
		})
	}
}
func TestVerifyArchiveCleansTemporaryDirectoryOnFailure(t *testing.T) {
	original := makeArchiveTempDir
	var created string
	makeArchiveTempDir = func(dir, pattern string) (string, error) {
		root, err := os.MkdirTemp(dir, pattern)
		created = root
		return root, err
	}
	t.Cleanup(func() { makeArchiveTempDir = original })
	_ = VerifyArchive(context.Background(), "oci-layout.tar.gz", bytes.NewReader(archiveBytes(t, map[string]byte{"../escape": tar.TypeReg})))
	if created == "" {
		t.Fatal("temporary directory was not created")
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("temporary directory remains: %v", err)
	}
}
func TestVerifyArchiveRejectsOversizedPAXMetadata(t *testing.T) {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	err := tw.WriteHeader(&tar.Header{Name: "x", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg, PAXRecords: map[string]string{"comment": strings.Repeat("x", int(maxArchiveStreamBytes+1))}})
	if err == nil {
		_, _ = tw.Write([]byte("x"))
	}
	_ = tw.Close()
	_ = gz.Close()
	if err := VerifyArchive(context.Background(), "oci-layout.tar.gz", bytes.NewReader(b.Bytes())); err == nil {
		t.Fatal("oversized PAX metadata accepted")
	}
}
func TestVerifyArchiveAcceptsVerifiedLayout(t *testing.T) {
	root := buildLayout(t)
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	err := filepath.Walk(root, func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if name == root {
			return nil
		}
		rel, _ := filepath.Rel(root, name)
		h, _ := tar.FileInfoHeader(info, "")
		h.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			data, _ := os.ReadFile(name)
			_, err = tw.Write(data)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()
	if err := VerifyArchive(context.Background(), "oci-layout.tar.gz", bytes.NewReader(b.Bytes())); err != nil {
		t.Fatal(err)
	}
}

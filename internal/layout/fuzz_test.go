package layout

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CYPT71/platform-factory/oci"
)

// FuzzVerifyIndex feeds arbitrary bytes as index.json into an otherwise
// real, valid OCI layout and calls the real Verify entrypoint. This is
// the layout parser's most exposed attack surface (T05/T06 in the Threat
// Model): index.json is the first untrusted file Verify reads, from a
// layout that may have been produced by an untrusted source rather than
// this project's own builder.
func FuzzVerifyIndex(f *testing.F) {
	dir := f.TempDir()
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		f.Fatal(err)
	}
	root := filepath.Join(dir, "layout")
	if _, err := oci.Build(oci.Options{Binary: binary, Output: root}); err != nil {
		f.Fatal(err)
	}
	indexPath := filepath.Join(root, "index.json")
	original, err := os.ReadFile(indexPath)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(original)
	f.Add([]byte(`{"schemaVersion":2,"manifests":[{"mediaType":"x","digest":"sha256:00","size":-1}]}`))
	f.Add([]byte(`{"schemaVersion":2,"manifests":[{"digest":"../../../etc/passwd"}]}`))
	f.Add([]byte("null"))
	f.Add([]byte("{}"))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		if err := os.WriteFile(indexPath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		_, _ = Verify(root)
	})
}

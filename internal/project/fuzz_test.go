package project

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzLoad feeds arbitrary bytes as a project config file (YAML or JSON -
// the decoder accepts both) at Load. A project config is user-authored and
// checked into an arbitrary repository secure-oci is pointed at, so it is
// untrusted input by construction.
func FuzzLoad(f *testing.F) {
	dir := f.TempDir()
	path := filepath.Join(dir, ".config_image.yaml")

	if real, err := os.ReadFile("../../examples/project-config/.config_image.yaml"); err == nil {
		f.Add(real)
	}
	f.Add([]byte(`version: 1`))
	f.Add([]byte(`{"version": 1, "language": "go"}`))
	f.Add([]byte(`version: 1
project: ../../../etc`))
	f.Add([]byte(`null`))
	f.Add([]byte(``))
	f.Add([]byte("version: [1, 2, 3]"))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		_, _ = Load(path)
	})
}

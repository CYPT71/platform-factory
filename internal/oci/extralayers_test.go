package oci

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CYPT71/secure-oci-base/internal/layout"
)

// writeTestTar builds an uncompressed tar file at path from the given
// entries - the shape a language plugin's build-layer subcommand is
// expected to produce.
func writeTestTar(t *testing.T, path string, entries []tar.Header, contents map[string][]byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	tw := tar.NewWriter(file)
	for _, header := range entries {
		h := header
		if data, ok := contents[header.Name]; ok {
			h.Size = int64(len(data))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if data, ok := contents[header.Name]; ok {
			if _, err := tw.Write(data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildWithExtraLayerAddsAnIndependentlyVerifiableLayer(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("binary-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	pluginTar := filepath.Join(dir, "python-runtime.tar")
	writeTestTar(t, pluginTar, []tar.Header{
		{Name: "runtime/python/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: time.Unix(0, 0)},
		{Name: "runtime/python/python3", Typeflag: tar.TypeReg, Mode: 0o755, ModTime: time.Unix(0, 0)},
	}, map[string][]byte{
		"runtime/python/python3": []byte("#!/bin/sh\necho python\n"),
	})

	output := filepath.Join(dir, "image")
	digest, err := Build(Options{
		Binary: binary, Output: output, Architecture: "amd64",
		ImageName: "example/service", Tag: "v1", Created: time.Unix(0, 0),
		ExtraLayers: []string{pluginTar},
	})
	if err != nil {
		t.Fatal(err)
	}

	report, err := layout.Verify(output)
	if err != nil || !report.Valid {
		t.Fatalf("independent verification failed: report=%+v err=%v", report, err)
	}

	manifestData, err := os.ReadFile(blobPath(output, digest))
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(manifestData, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Layers) != 2 {
		t.Fatalf("layers=%d, want 2 (entrypoint layer + plugin layer)", len(m.Layers))
	}
	configData, err := os.ReadFile(blobPath(output, m.Config.Digest))
	if err != nil {
		t.Fatal(err)
	}
	var config imageConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.RootFS.DiffIDs) != 2 {
		t.Fatalf("diff_ids=%v, want 2 entries", config.RootFS.DiffIDs)
	}

	// The second layer's content is exactly what writeTestTar wrote -
	// prove Build re-serializes the same bytes it validated, not some
	// other representation of them.
	layerFile, err := os.Open(blobPath(output, m.Layers[1].Digest))
	if err != nil {
		t.Fatal(err)
	}
	defer layerFile.Close()
	gz, err := gzip.NewReader(layerFile)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := false
	for {
		h, nextErr := tr.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if h.Name == "runtime/python/python3" {
			found = true
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "#!/bin/sh\necho python\n" {
				t.Fatalf("content=%q", data)
			}
		}
	}
	if !found {
		t.Fatal("runtime/python/python3 not found in the plugin-supplied layer")
	}
}

func TestBuildExtraLayerDeterministic(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("binary-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	pluginTar := filepath.Join(dir, "runtime.tar")
	entries := []tar.Header{
		{Name: "runtime/lib.so", Typeflag: tar.TypeReg, Mode: 0o644, ModTime: time.Unix(0, 0)},
	}
	contents := map[string][]byte{"runtime/lib.so": []byte("library bytes")}
	writeTestTar(t, pluginTar, entries, contents)

	var digests []string
	for i := 0; i < 2; i++ {
		output := filepath.Join(dir, "image"+string(rune('a'+i)))
		digest, err := Build(Options{
			Binary: binary, Output: output, Architecture: "amd64", Created: time.Unix(0, 0),
			ExtraLayers: []string{pluginTar},
		})
		if err != nil {
			t.Fatal(err)
		}
		digests = append(digests, digest)
	}
	if digests[0] != digests[1] {
		t.Fatalf("non-deterministic manifest digest: %v", digests)
	}
}

func TestBuildRejectsUnsafeExtraLayerEntries(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("binary-data"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := map[string]tar.Header{
		"absolute path": {Name: "/etc/passwd", Typeflag: tar.TypeReg, Mode: 0o644, ModTime: time.Unix(0, 0)},
		"traversal":     {Name: "../../etc/passwd", Typeflag: tar.TypeReg, Mode: 0o644, ModTime: time.Unix(0, 0)},
		"symlink":       {Name: "evil", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd", ModTime: time.Unix(0, 0)},
		"hardlink":      {Name: "evil", Typeflag: tar.TypeLink, Linkname: "runtime/lib.so", ModTime: time.Unix(0, 0)},
		"setuid":        {Name: "evil", Typeflag: tar.TypeReg, Mode: 0o4755, ModTime: time.Unix(0, 0)},
		"device":        {Name: "evil", Typeflag: tar.TypeChar, Mode: 0o644, ModTime: time.Unix(0, 0)},
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			pluginTar := filepath.Join(t.TempDir(), "runtime.tar")
			writeTestTar(t, pluginTar, []tar.Header{header}, nil)
			output := filepath.Join(t.TempDir(), "image")
			_, err := Build(Options{
				Binary: binary, Output: output, Architecture: "amd64", Created: time.Unix(0, 0),
				ExtraLayers: []string{pluginTar},
			})
			if err == nil {
				t.Fatalf("%s: expected rejection", name)
			}
		})
	}
}

func TestBuildRejectsMissingExtraLayerFile(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("binary-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "image")
	_, err := Build(Options{
		Binary: binary, Output: output, Architecture: "amd64", Created: time.Unix(0, 0),
		ExtraLayers: []string{filepath.Join(dir, "does-not-exist.tar")},
	})
	if err == nil {
		t.Fatal("expected an error for a missing extra layer file")
	}
}

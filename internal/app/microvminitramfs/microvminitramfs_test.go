package microvminitramfs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// minimalOCILayout writes a self-contained, valid single-manifest OCI image
// layout containing one regular file at path "app". internal/rootfs.Convert
// only requires tar/gzip/JSON structure, not a real ELF binary, so this
// avoids needing a cross-compiled fixture. A local copy of
// cmd/microvm-initramfs/main_test.go's identically-named helper.
func minimalOCILayout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	blobDir := filepath.Join(root, "blobs", "sha256")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBlob := func(media string, data []byte) map[string]any {
		sum := sha256.Sum256(data)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		if err := os.WriteFile(filepath.Join(blobDir, hex.EncodeToString(sum[:])), data, 0o644); err != nil {
			t.Fatal(err)
		}
		return map[string]any{"mediaType": media, "digest": digest, "size": len(data)}
	}

	var rawLayer bytes.Buffer
	archive := tar.NewWriter(&rawLayer)
	header := &tar.Header{Name: "app", Mode: 0o755, Size: 7, Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0)}
	if err := archive.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	rawSum := sha256.Sum256(rawLayer.Bytes())
	diffID := "sha256:" + hex.EncodeToString(rawSum[:])

	var compressedLayer bytes.Buffer
	gz := gzip.NewWriter(&compressedLayer)
	if _, err := gz.Write(rawLayer.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	layerDesc := writeBlob("application/vnd.oci.image.layer.v1.tar+gzip", compressedLayer.Bytes())

	configData, _ := json.Marshal(map[string]any{
		"os": "linux", "architecture": "amd64",
		"rootfs": map[string]any{"type": "layers", "diff_ids": []string{diffID}},
	})
	configDesc := writeBlob("application/vnd.oci.image.config.v1+json", configData)

	manifestData, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "config": configDesc, "layers": []any{layerDesc},
	})
	manifestDesc := writeBlob("application/vnd.oci.image.manifest.v1+json", manifestData)
	manifestDesc["platform"] = map[string]any{"os": "linux", "architecture": "amd64"}
	manifestDesc["annotations"] = map[string]string{"org.opencontainers.image.ref.name": "test:latest"}

	indexData, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []any{manifestDesc}})
	if err := os.WriteFile(filepath.Join(root, "index.json"), indexData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "oci-layout"), []byte("{\"imageLayoutVersion\":\"1.0.0\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeStubInit(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "microvm-init")
	if err := os.WriteFile(path, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAssembleEndToEnd(t *testing.T) {
	layout := minimalOCILayout(t)
	initBinary := writeStubInit(t)
	output := filepath.Join(t.TempDir(), "initramfs.cpio.gz")

	result, err := Assemble(layout, "linux/amd64", "", initBinary, []string{"/app/service"}, output)
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 1 || result.RootFSDigest == "" || result.ManifestDigest == "" || result.InitramfsBytes == 0 {
		t.Fatalf("result=%+v", result)
	}
	info, err := os.Stat(output)
	if err != nil || info.Size() == 0 {
		t.Fatalf("output missing or empty: err=%v info=%v", err, info)
	}
}

func TestAssembleRejectsExistingOutput(t *testing.T) {
	layout := minimalOCILayout(t)
	initBinary := writeStubInit(t)
	output := filepath.Join(t.TempDir(), "initramfs.cpio.gz")
	if err := os.WriteFile(output, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Assemble(layout, "linux/amd64", "", initBinary, nil, output); err == nil {
		t.Fatal("expected an error for a pre-existing output")
	}
}

func TestAssembleRejectsInvalidLayout(t *testing.T) {
	invalidLayout := t.TempDir() // no index.json / oci-layout marker
	initBinary := writeStubInit(t)
	output := filepath.Join(t.TempDir(), "initramfs.cpio.gz")
	if _, err := Assemble(invalidLayout, "linux/amd64", "", initBinary, nil, output); err == nil {
		t.Fatal("expected an error for an invalid layout")
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("output installed despite convert failure: err=%v", statErr)
	}
}

func TestAssembleRejectsMissingInitBinary(t *testing.T) {
	layout := minimalOCILayout(t)
	output := filepath.Join(t.TempDir(), "initramfs.cpio.gz")
	if _, err := Assemble(layout, "linux/amd64", "", filepath.Join(t.TempDir(), "missing-init"), nil, output); err == nil {
		t.Fatal("expected an error for a missing init binary")
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("output installed despite install-init failure: err=%v", statErr)
	}
}

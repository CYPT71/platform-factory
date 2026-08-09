package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// minimalOCILayout writes a self-contained, valid single-manifest OCI image
// layout containing one regular file at path "app". internal/rootfs.Convert
// only requires tar/gzip/JSON structure, not a real ELF binary, so this
// avoids needing a cross-compiled fixture.
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

func TestRunAssemblesInitramfsEndToEnd(t *testing.T) {
	layout := minimalOCILayout(t)
	initBinary := writeStubInit(t)
	output := filepath.Join(t.TempDir(), "initramfs.cpio.gz")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-layout", layout, "-init", initBinary, "-output", output,
		"-entrypoint", "/app/service",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	info, err := os.Stat(output)
	if err != nil || info.Size() == 0 {
		t.Fatalf("output missing or empty: err=%v info=%v", err, info)
	}
	var decoded assembleResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if decoded.Files != 1 || decoded.RootFSDigest == "" || decoded.ManifestDigest == "" || decoded.InitramfsBytes == 0 {
		t.Fatalf("decoded=%+v", decoded)
	}

	gz, err := gzip.NewReader(bytes.NewReader(mustReadFile(t, output)))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	archive := tar.NewReader(gz)
	_, tarErr := archive.Next()
	if tarErr == nil {
		t.Fatal("cpio archive parsed as tar; expected non-tar cpio content")
	}
}

func TestRunRejectsMissingRequiredFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestRunRejectsInvalidLayout(t *testing.T) {
	invalidLayout := t.TempDir() // no index.json / oci-layout marker
	initBinary := writeStubInit(t)
	output := filepath.Join(t.TempDir(), "initramfs.cpio.gz")
	var stdout, stderr bytes.Buffer
	code := run([]string{"-layout", invalidLayout, "-init", initBinary, "-output", output}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "convert rootfs") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("output installed despite convert failure: err=%v", err)
	}
}

func TestRunRejectsMissingInitBinary(t *testing.T) {
	layout := minimalOCILayout(t)
	output := filepath.Join(t.TempDir(), "initramfs.cpio.gz")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-layout", layout, "-init", filepath.Join(t.TempDir(), "missing-init"), "-output", output,
	}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "install init") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("output installed despite install-init failure: err=%v", err)
	}
}

func TestRunRejectsExistingOutput(t *testing.T) {
	layout := minimalOCILayout(t)
	initBinary := writeStubInit(t)
	output := filepath.Join(t.TempDir(), "initramfs.cpio.gz")
	if err := os.WriteFile(output, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"-layout", layout, "-init", initBinary, "-output", output}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func mustReadFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

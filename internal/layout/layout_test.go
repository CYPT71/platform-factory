package layout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/oci"
)

func buildLayout(t *testing.T) string {
	return buildNamedLayout(t, "secure-oci-base", "latest", "amd64", "payload")
}

func buildNamedLayout(t *testing.T, image, tag, architecture, payload string) string {
	t.Helper()
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte(payload), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "layout")
	if _, err := oci.Build(oci.Options{
		Binary: binary, Output: output, Architecture: architecture,
		ImageName: image, Tag: tag,
	}); err != nil {
		t.Fatal(err)
	}
	return output
}

func TestComposeMultiArchitectureAndMultipleImages(t *testing.T) {
	amd64 := buildNamedLayout(t, "example/api", "v1", "amd64", "amd64")
	arm64 := buildNamedLayout(t, "example/api", "v1", "arm64", "arm64")
	worker := buildNamedLayout(t, "example/worker", "stable", "amd64", "worker")
	output := filepath.Join(t.TempDir(), "combined")

	report, err := Compose(output, []string{worker, arm64, amd64})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Valid || report.Manifests != 3 || len(report.Platforms) != 3 {
		t.Fatalf("report = %+v", report)
	}
	references := map[string]int{}
	for _, platform := range report.Platforms {
		references[platform.Reference]++
	}
	if references["example/api:v1"] != 2 || references["example/worker:stable"] != 1 {
		t.Fatalf("references = %#v", references)
	}
	if verified, err := Verify(output); err != nil || verified.Manifests != 3 {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}
}

func TestComposeMultipleTagsDeduplicatesBlobs(t *testing.T) {
	first := buildNamedLayout(t, "example/api", "v1", "amd64", "same")
	second := buildNamedLayout(t, "example/api", "latest", "amd64", "same")
	output := filepath.Join(t.TempDir(), "combined")
	report, err := Compose(output, []string{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if report.Manifests != 2 || report.Blobs != 3 {
		t.Fatalf("expected two references sharing three blobs, report=%+v", report)
	}
}

func TestComposeRejectsInvalidInputsAndExistingOutput(t *testing.T) {
	valid := buildLayout(t)
	root := t.TempDir()
	invalid := filepath.Join(root, "invalid")
	if err := os.Mkdir(invalid, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Compose(filepath.Join(root, "few"), []string{valid}); err == nil {
		t.Fatal("single input accepted")
	}
	if _, err := Compose(filepath.Join(root, "bad"), []string{valid, invalid}); err == nil {
		t.Fatal("invalid input accepted")
	}
	existing := filepath.Join(root, "existing")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Compose(existing, []string{valid, valid}); err == nil {
		t.Fatal("existing output accepted")
	}
	if _, err := Compose("", []string{valid, valid}); err == nil {
		t.Fatal("empty output accepted")
	}
	parentFile := filepath.Join(root, "parent-file")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Compose(filepath.Join(parentFile, "output"), []string{valid, valid}); err == nil {
		t.Fatal("non-directory output parent accepted")
	}
	if _, err := Compose(filepath.Join(root, "duplicate"), []string{valid, valid}); err == nil ||
		!strings.Contains(err.Error(), "duplicate reference/platform") {
		t.Fatalf("duplicate reference/platform error = %v", err)
	}
}

func TestCopyBlobErrors(t *testing.T) {
	root := t.TempDir()
	buffer := make([]byte, 8)
	if err := copyBlob(filepath.Join(root, "missing"), filepath.Join(root, "output"), buffer); err == nil {
		t.Fatal("missing source accepted")
	}
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(root, "existing")
	if err := os.WriteFile(existing, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyBlob(source, existing, buffer); err == nil {
		t.Fatal("existing destination accepted")
	}
}

func TestVerifyAndInspectLayout(t *testing.T) {
	root := buildLayout(t)
	report, err := Verify(root)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Valid || report.Manifests != 1 || report.Blobs != 3 ||
		len(report.Platforms) != 1 || report.Platforms[0].Architecture != "amd64" {
		t.Fatalf("report = %+v", report)
	}
	inspected, err := Inspect(root)
	if err != nil || inspected.Platforms[0].Digest == "" {
		t.Fatalf("inspect=%+v err=%v", inspected, err)
	}
}

func TestVerifyForLocalImportSkipsTheSecretScanButKeepsEveryOtherCheck(t *testing.T) {
	root := buildNamedLayout(t, "example/api", "v1", "amd64", "password=hunter2")
	if _, err := Verify(root); err == nil {
		t.Fatal("Verify should still reject a layer carrying a real secret-shaped marker")
	}
	report, err := VerifyForLocalImport(root)
	if err != nil {
		t.Fatalf("VerifyForLocalImport should accept the same layout: %v", err)
	}
	if !report.Valid || report.Manifests != 1 {
		t.Fatalf("report = %+v", report)
	}

	// Every other check VerifyForLocalImport shares with Verify must
	// still reject corruption - only the secret scan is skipped.
	corrupt := buildNamedLayout(t, "example/api", "v1", "amd64", "password=hunter2")
	entries, err := os.ReadDir(filepath.Join(corrupt, "blobs", "sha256"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt, "blobs", "sha256", entries[0].Name()), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyForLocalImport(corrupt); err == nil {
		t.Fatal("VerifyForLocalImport should still reject a corrupted blob")
	}
}

func TestVerifyRejectsCorruptionAndUnexpectedBlobs(t *testing.T) {
	for _, mutate := range []func(string){
		func(root string) { _ = os.WriteFile(filepath.Join(root, "oci-layout"), []byte("{}"), 0o644) },
		func(root string) {
			_ = os.WriteFile(filepath.Join(root, "blobs", "sha256", strings.Repeat("a", 64)), []byte("x"), 0o644)
		},
		func(root string) {
			entries, _ := os.ReadDir(filepath.Join(root, "blobs", "sha256"))
			_ = os.WriteFile(filepath.Join(root, "blobs", "sha256", entries[0].Name()), []byte("corrupt"), 0o644)
		},
	} {
		root := buildLayout(t)
		mutate(root)
		if _, err := Verify(root); err == nil {
			t.Fatal("corrupted layout accepted")
		}
	}
}

func TestDecodeFile(t *testing.T) {
	dir := t.TempDir()

	valid := filepath.Join(dir, "valid.json")
	if err := os.WriteFile(valid, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]int
	if err := decodeFile(valid, &decoded); err != nil {
		t.Fatalf("decodeFile(valid) = %v", err)
	}
	if decoded["a"] != 1 {
		t.Fatalf("decoded=%v, want a=1", decoded)
	}

	malformed := filepath.Join(dir, "malformed.json")
	if err := os.WriteFile(malformed, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := decodeFile(malformed, &decoded); err == nil {
		t.Fatal("decodeFile accepted malformed JSON")
	}

	if err := decodeFile(filepath.Join(dir, "missing.json"), &decoded); err == nil {
		t.Fatal("decodeFile accepted a missing file")
	}

	notRegular := filepath.Join(dir, "subdir")
	if err := os.Mkdir(notRegular, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := decodeFile(notRegular, &decoded); err == nil {
		t.Fatal("decodeFile accepted a directory")
	}
}

func TestVerifyRejectsMissingAndNonDirectoryRoots(t *testing.T) {
	if _, err := Verify(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing root accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, []byte("x"), 0o644)
	if _, err := Verify(file); err == nil {
		t.Fatal("file root accepted")
	}
}

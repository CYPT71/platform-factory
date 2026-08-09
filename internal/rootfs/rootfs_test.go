package rootfs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testEntry struct {
	name string
	body string
	link string
	mode int64
	kind byte
}

func TestConvertRejectsMissingLayoutOrOutput(t *testing.T) {
	if _, err := Convert(Options{Output: t.TempDir()}); err == nil {
		t.Fatal("empty layout accepted")
	}
	if _, err := Convert(Options{Layout: t.TempDir()}); err == nil {
		t.Fatal("empty output accepted")
	}
}

func TestConvertRejectsNonexistentLayout(t *testing.T) {
	if _, err := Convert(Options{
		Layout: filepath.Join(t.TempDir(), "missing"), Output: filepath.Join(t.TempDir(), "rootfs"),
	}); err == nil {
		t.Fatal("nonexistent layout accepted")
	}
}

func TestConvertRejectsOutputThatAlreadyExists(t *testing.T) {
	layout := writeTestLayout(t, []testEntry{{name: "app", body: "x", kind: tar.TypeReg}})
	output := t.TempDir() // already exists as a directory
	if _, err := Convert(Options{Layout: layout, Output: output}); err == nil {
		t.Fatal("existing output accepted")
	}
}

func TestConvertRejectsInvalidOrMissingMarker(t *testing.T) {
	for name, mutate := range map[string]func(layout string){
		"missing": func(layout string) {
			if err := os.Remove(filepath.Join(layout, "oci-layout")); err != nil {
				t.Fatal(err)
			}
		},
		"wrong content": func(layout string) {
			if err := os.WriteFile(filepath.Join(layout, "oci-layout"), []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"not a regular file": func(layout string) {
			if err := os.Remove(filepath.Join(layout, "oci-layout")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(layout, "oci-layout"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			layout := writeTestLayout(t, []testEntry{{name: "app", body: "x", kind: tar.TypeReg}})
			mutate(layout)
			if _, err := Convert(Options{Layout: layout, Output: filepath.Join(t.TempDir(), "rootfs")}); err == nil {
				t.Fatal("invalid marker accepted")
			}
		})
	}
}

func TestConvertRejectsIndexSchemaVersionMismatch(t *testing.T) {
	layout := writeTestLayout(t, []testEntry{{name: "app", body: "x", kind: tar.TypeReg}})
	var idx index
	readTestJSON(t, filepath.Join(layout, "index.json"), &idx)
	idx.SchemaVersion = 1
	writeTestJSON(t, filepath.Join(layout, "index.json"), idx)
	if _, err := Convert(Options{Layout: layout, Output: filepath.Join(t.TempDir(), "rootfs")}); err == nil {
		t.Fatal("wrong index schemaVersion accepted")
	}
}

func TestConvertRejectsInvalidManifest(t *testing.T) {
	layout := writeTestLayout(t, []testEntry{{name: "app", body: "x", kind: tar.TypeReg}})
	var idx index
	readTestJSON(t, filepath.Join(layout, "index.json"), &idx)
	// A syntactically valid but structurally wrong manifest (no
	// schemaVersion, no layers); rewritten with a matching digest so the
	// failure under test is manifest *shape* validation, not the
	// (already covered) digest mismatch check.
	manifestData, err := json.Marshal(map[string]any{"not": "a manifest"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testBlobPath(layout, idx.Manifests[0].Digest), manifestData, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(manifestData)
	idx.Manifests[0].Digest = "sha256:" + hex.EncodeToString(sum[:])
	idx.Manifests[0].Size = int64(len(manifestData))
	writeTestJSON(t, filepath.Join(layout, "index.json"), idx)
	if _, err := Convert(Options{Layout: layout, Output: filepath.Join(t.TempDir(), "rootfs")}); err == nil {
		t.Fatal("invalid manifest shape accepted")
	}
}

func TestConvertRejectsInvalidConfig(t *testing.T) {
	layout := writeTestLayout(t, []testEntry{{name: "app", body: "x", kind: tar.TypeReg}})
	var idx index
	readTestJSON(t, filepath.Join(layout, "index.json"), &idx)
	var document manifest
	if err := json.Unmarshal(readTestBlob(t, layout, idx.Manifests[0].Digest), &document); err != nil {
		t.Fatal(err)
	}

	// Rewrite the config blob with a wrong rootfs.type, then re-point
	// both descriptors at the new digests so the only thing under test
	// is config *shape* validation, not a digest mismatch.
	configData := []byte(`{"os":"linux","architecture":"amd64","rootfs":{"type":"not-layers","diff_ids":[]}}`)
	if err := os.WriteFile(testBlobPath(layout, document.Config.Digest), configData, 0o644); err != nil {
		t.Fatal(err)
	}
	configSum := sha256.Sum256(configData)
	document.Config.Digest = "sha256:" + hex.EncodeToString(configSum[:])
	document.Config.Size = int64(len(configData))

	manifestData, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testBlobPath(layout, idx.Manifests[0].Digest), manifestData, 0o644); err != nil {
		t.Fatal(err)
	}
	manifestSum := sha256.Sum256(manifestData)
	idx.Manifests[0].Digest = "sha256:" + hex.EncodeToString(manifestSum[:])
	idx.Manifests[0].Size = int64(len(manifestData))
	writeTestJSON(t, filepath.Join(layout, "index.json"), idx)

	if _, err := Convert(Options{Layout: layout, Output: filepath.Join(t.TempDir(), "rootfs")}); err == nil {
		t.Fatal("rootfs.type mismatch accepted")
	}
}

func TestConvertRejectsUnwritableOutputParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permission bits")
	}
	layout := writeTestLayout(t, []testEntry{{name: "app", body: "x", kind: tar.TypeReg}})
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	if _, err := Convert(Options{Layout: layout, Output: filepath.Join(parent, "rootfs")}); err == nil {
		t.Fatal("unwritable output parent accepted")
	}
}

func TestSelectManifestRejectsNonLinuxPlatform(t *testing.T) {
	_, err := selectManifest([]descriptor{{
		Digest:   "sha256:" + strings.Repeat("0", 64),
		Platform: &platform{OS: "windows", Architecture: "amd64"},
	}}, "", "")
	if err == nil {
		t.Fatal("non-linux platform accepted")
	}
}

func TestNewExtractionBudgetRejectsNegativeValues(t *testing.T) {
	for _, opts := range []Options{
		{MaxBytes: -1}, {MaxFiles: -1}, {MaxFileBytes: -1},
	} {
		if _, err := newExtractionBudget(opts); err == nil {
			t.Fatalf("negative budget %+v accepted", opts)
		}
	}
}

func TestParseDigestRejectsInvalidInput(t *testing.T) {
	for _, value := range []string{
		"", "sha256:short", "md5:" + strings.Repeat("0", 60),
		"sha256:" + strings.Repeat("z", 64),
	} {
		if _, err := parseDigest(value); err == nil {
			t.Fatalf("parseDigest(%q) accepted", value)
		}
	}
}

func TestOpenBlobRejectsInvalidDescriptors(t *testing.T) {
	dir := t.TempDir()
	blobDir := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	validDigest := "sha256:" + strings.Repeat("a", 64)
	if err := os.WriteFile(filepath.Join(blobDir, strings.Repeat("a", 64)), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := openBlob(dir, descriptor{Digest: "bad", Size: 4}); err == nil {
		t.Fatal("invalid digest accepted")
	}
	if _, err := openBlob(dir, descriptor{Digest: validDigest, Size: -1}); err == nil {
		t.Fatal("negative size accepted")
	}
	missingDigest := "sha256:" + strings.Repeat("b", 64)
	if _, err := openBlob(dir, descriptor{Digest: missingDigest, Size: 4}); err == nil {
		t.Fatal("missing blob accepted")
	}
	if _, err := openBlob(dir, descriptor{Digest: validDigest, Size: 999}); err == nil {
		t.Fatal("wrong declared size accepted")
	}
	dirDigest := "sha256:" + strings.Repeat("c", 64)
	if err := os.Mkdir(filepath.Join(blobDir, strings.Repeat("c", 64)), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := openBlob(dir, descriptor{Digest: dirDigest, Size: 0}); err == nil {
		t.Fatal("directory blob accepted")
	}
}

func TestReadVerifiedBlobRejectsOversizedAndMismatchedContent(t *testing.T) {
	dir := t.TempDir()
	blobDir := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	validDigest := "sha256:" + strings.Repeat("a", 64)
	if err := os.WriteFile(filepath.Join(blobDir, strings.Repeat("a", 64)), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := readVerifiedBlob(dir, descriptor{Digest: validDigest, Size: maxJSONBytes + 1}); err == nil {
		t.Fatal("oversized descriptor accepted")
	}
	// validDigest is not "data"'s real sha256, so the content hash check
	// must fail even though the declared size matches.
	if _, err := readVerifiedBlob(dir, descriptor{Digest: validDigest, Size: 4}); err == nil {
		t.Fatal("mismatched digest accepted")
	}
}

func TestReadJSONFileRejectsInvalidInputs(t *testing.T) {
	dir := t.TempDir()
	var target any

	missing := filepath.Join(dir, "missing.json")
	if err := readJSONFile(missing, &target); err == nil {
		t.Fatal("missing file accepted")
	}

	notRegular := filepath.Join(dir, "adir")
	if err := os.Mkdir(notRegular, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := readJSONFile(notRegular, &target); err == nil {
		t.Fatal("directory accepted as a JSON file")
	}

	trailing := filepath.Join(dir, "trailing.json")
	if err := os.WriteFile(trailing, []byte(`{} {}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := readJSONFile(trailing, &target); err == nil {
		t.Fatal("trailing JSON data accepted")
	}
}

func TestConvertAppliesLayersAndWhiteoutsDeterministically(t *testing.T) {
	first := []testEntry{
		{name: "etc/", mode: 0o755, kind: tar.TypeDir},
		{name: "etc/old", body: "old", mode: 0o644, kind: tar.TypeReg},
		{name: "app", body: "v1", mode: 0o755, kind: tar.TypeReg},
	}
	second := []testEntry{
		{name: "etc/.wh.old", mode: 0o600, kind: tar.TypeReg},
		{name: "app", body: "v2", mode: 0o755, kind: tar.TypeReg},
	}
	layout := writeTestLayout(t, first, second)
	output := filepath.Join(t.TempDir(), "rootfs")
	result, err := Convert(Options{Layout: layout, Output: output})
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(output, "app")); err != nil || string(data) != "v2" {
		t.Fatalf("app=%q err=%v", data, err)
	}
	if _, err := os.Lstat(filepath.Join(output, "etc", "old")); !os.IsNotExist(err) {
		t.Fatalf("whiteouted file remains: %v", err)
	}
	if result.Files != 1 || result.Bytes != 2 || result.RootFSDigest == "" {
		t.Fatalf("result=%+v", result)
	}

	secondOutput := filepath.Join(t.TempDir(), "rootfs")
	secondResult, err := Convert(Options{Layout: layout, Output: secondOutput})
	if err != nil {
		t.Fatal(err)
	}
	if secondResult.RootFSDigest != result.RootFSDigest {
		t.Fatalf("non-deterministic rootfs: %s vs %s", result.RootFSDigest, secondResult.RootFSDigest)
	}
}

func TestConvertRejectsTraversalWithoutInstallingOutput(t *testing.T) {
	layout := writeTestLayout(t, []testEntry{
		{name: "../escape", body: "bad", mode: 0o644, kind: tar.TypeReg},
	})
	parent := t.TempDir()
	output := filepath.Join(parent, "rootfs")
	if _, err := Convert(Options{Layout: layout, Output: output}); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Fatalf("partial output installed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(parent, "escape")); !os.IsNotExist(err) {
		t.Fatalf("file escaped rootfs: %v", err)
	}
}

func TestConvertRejectsCorruptedLayerDigest(t *testing.T) {
	layout := writeTestLayout(t, []testEntry{
		{name: "app", body: "good", mode: 0o755, kind: tar.TypeReg},
	})
	var idx index
	readTestJSON(t, filepath.Join(layout, "index.json"), &idx)
	manifestData := readTestBlob(t, layout, idx.Manifests[0].Digest)
	var document manifest
	if err := json.Unmarshal(manifestData, &document); err != nil {
		t.Fatal(err)
	}
	layerPath := testBlobPath(layout, document.Layers[0].Digest)
	file, err := os.OpenFile(layerPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{0xff}, document.Layers[0].Size-1); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	output := filepath.Join(t.TempDir(), "rootfs")
	if _, err := Convert(Options{Layout: layout, Output: output}); err == nil {
		t.Fatal("corrupted layer accepted")
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Fatalf("corrupted rootfs installed: %v", err)
	}
}

func TestConvertAppliesOpaqueWhiteout(t *testing.T) {
	layout := writeTestLayout(t,
		[]testEntry{
			{name: "data/", kind: tar.TypeDir, mode: 0o755},
			{name: "data/a", body: "a", kind: tar.TypeReg, mode: 0o644},
			{name: "data/b", body: "b", kind: tar.TypeReg, mode: 0o644},
		},
		[]testEntry{
			{name: "data/.wh..wh..opq", kind: tar.TypeReg, mode: 0o600},
			{name: "data/c", body: "c", kind: tar.TypeReg, mode: 0o644},
		},
	)
	output := filepath.Join(t.TempDir(), "rootfs")
	if _, err := Convert(Options{Layout: layout, Output: output}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(output, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "c" {
		t.Fatalf("opaque directory contains %v", entries)
	}
}

func TestConvertSupportsSafeLinksAndRejectsEscapes(t *testing.T) {
	layout := writeTestLayout(t, []testEntry{
		{name: "bin/", mode: 0o755, kind: tar.TypeDir},
		{name: "bin/app", body: "payload", mode: 0o755, kind: tar.TypeReg},
		{name: "bin/current", link: "app", mode: 0o777, kind: tar.TypeSymlink},
		{name: "bin/copy", link: "bin/app", mode: 0o755, kind: tar.TypeLink},
	})
	output := filepath.Join(t.TempDir(), "rootfs")
	if _, err := Convert(Options{Layout: layout, Output: output}); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(output, "bin/current")); err != nil || target != "app" {
		t.Fatalf("symlink target=%q err=%v", target, err)
	}
	original, err := os.Stat(filepath.Join(output, "bin/app"))
	if err != nil {
		t.Fatal(err)
	}
	copyInfo, err := os.Stat(filepath.Join(output, "bin/copy"))
	if err != nil || !os.SameFile(original, copyInfo) {
		t.Fatalf("hardlink not preserved: err=%v", err)
	}

	for index, entry := range []testEntry{
		{name: "bad", link: "/etc/passwd", kind: tar.TypeSymlink},
		{name: "dir/bad", link: "../../escape", kind: tar.TypeSymlink},
		{name: "bad", link: "../escape", kind: tar.TypeLink},
	} {
		layout := writeTestLayout(t, []testEntry{entry})
		if _, err := Convert(Options{
			Layout: layout, Output: filepath.Join(t.TempDir(), "rootfs"),
		}); err == nil {
			t.Fatalf("unsafe link case %d accepted", index)
		}
	}
}

func TestConvertRejectsBlobSymlink(t *testing.T) {
	layout := writeTestLayout(t, []testEntry{{name: "app", body: "x", kind: tar.TypeReg}})
	var idx index
	readTestJSON(t, filepath.Join(layout, "index.json"), &idx)
	blob := testBlobPath(layout, idx.Manifests[0].Digest)
	backup := blob + ".real"
	if err := os.Rename(blob, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backup, blob); err != nil {
		t.Fatal(err)
	}
	if _, err := Convert(Options{Layout: layout, Output: filepath.Join(t.TempDir(), "rootfs")}); err == nil {
		t.Fatal("symlink blob accepted")
	}
}

func TestConvertRequiresUnambiguousManifest(t *testing.T) {
	layout := writeTestLayout(t, []testEntry{{name: "app", body: "x", kind: tar.TypeReg}})
	var idx index
	readTestJSON(t, filepath.Join(layout, "index.json"), &idx)
	idx.Manifests = append(idx.Manifests, idx.Manifests[0])
	writeTestJSON(t, filepath.Join(layout, "index.json"), idx)
	if _, err := Convert(Options{Layout: layout, Output: filepath.Join(t.TempDir(), "out")}); err == nil {
		t.Fatal("ambiguous manifest accepted")
	}
}

func TestConvertEnforcesExtractionBudgetsWithoutInstallingOutput(t *testing.T) {
	layout := writeTestLayout(t, []testEntry{
		{name: "a", body: "1234", kind: tar.TypeReg, mode: 0o644},
		{name: "b", body: "5678", kind: tar.TypeReg, mode: 0o644},
	})
	tests := []Options{
		{MaxBytes: 128, MaxFiles: 10, MaxFileBytes: 64},
		{MaxBytes: 1 << 20, MaxFiles: 1, MaxFileBytes: 64},
		{MaxBytes: 1 << 20, MaxFiles: 10, MaxFileBytes: 3},
	}
	for index, options := range tests {
		options.Layout = layout
		options.Output = filepath.Join(t.TempDir(), "rootfs")
		if _, err := Convert(options); err == nil {
			t.Fatalf("budget case %d accepted", index)
		}
		if _, err := os.Lstat(options.Output); !os.IsNotExist(err) {
			t.Fatalf("budget case %d installed partial output: %v", index, err)
		}
	}
	if _, err := Convert(Options{
		Layout: layout, Output: filepath.Join(t.TempDir(), "rootfs"),
		MaxBytes: 10, MaxFileBytes: 11,
	}); err == nil {
		t.Fatal("incoherent budgets accepted")
	}
}

func writeTestLayout(t *testing.T, layers ...[]testEntry) string {
	t.Helper()
	root := t.TempDir()
	blobDir := filepath.Join(root, "blobs", "sha256")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var layerDescriptors []descriptor
	var diffIDs []string
	for _, entries := range layers {
		compressed, raw := testLayer(t, entries)
		layerDescriptors = append(layerDescriptors, writeTestBlob(t, root,
			"application/vnd.oci.image.layer.v1.tar+gzip", compressed))
		rawSum := sha256.Sum256(raw)
		diffIDs = append(diffIDs, "sha256:"+hex.EncodeToString(rawSum[:]))
	}
	config := imageConfig{OS: "linux", Architecture: "amd64"}
	config.RootFS.Type = "layers"
	config.RootFS.DiffIDs = diffIDs
	configData, _ := json.Marshal(config)
	configDesc := writeTestBlob(t, root, "application/vnd.oci.image.config.v1+json", configData)
	document := manifest{SchemaVersion: 2, Config: configDesc, Layers: layerDescriptors}
	manifestData, _ := json.Marshal(document)
	manifestDesc := writeTestBlob(t, root, "application/vnd.oci.image.manifest.v1+json", manifestData)
	manifestDesc.Platform = &platform{OS: "linux", Architecture: "amd64"}
	manifestDesc.Annotations = map[string]string{"org.opencontainers.image.ref.name": "test:latest"}
	writeTestJSON(t, filepath.Join(root, "index.json"), index{SchemaVersion: 2, Manifests: []descriptor{manifestDesc}})
	if err := os.WriteFile(filepath.Join(root, "oci-layout"), []byte("{\"imageLayoutVersion\":\"1.0.0\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func testLayer(t *testing.T, entries []testEntry) ([]byte, []byte) {
	t.Helper()
	var raw bytes.Buffer
	archive := tar.NewWriter(&raw)
	for _, entry := range entries {
		kind := entry.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		header := &tar.Header{
			Name: entry.name, Mode: entry.mode, Size: int64(len(entry.body)),
			Typeflag: kind, Linkname: entry.link, ModTime: time.Unix(1234, 0),
		}
		if kind != tar.TypeReg && kind != tar.TypeRegA {
			header.Size = 0
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := archive.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	gz.ModTime = time.Unix(0, 0)
	if _, err := gz.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes(), raw.Bytes()
}

func writeTestBlob(t *testing.T, root, media string, data []byte) descriptor {
	t.Helper()
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if err := os.WriteFile(testBlobPath(root, digest), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return descriptor{MediaType: media, Digest: digest, Size: int64(len(data))}
}

func testBlobPath(root, digest string) string {
	return filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))
}

func TestApplyWhiteout(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "a"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	handled, err := applyWhiteout(root, "sub/.wh..wh..opq")
	if !handled || err != nil {
		t.Fatalf("opaque whiteout: handled=%v err=%v", handled, err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "sub"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("opaque whiteout did not clear sub/: entries=%v err=%v", entries, err)
	}

	handled, err = applyWhiteout(root, "missing/.wh..wh..opq")
	if !handled || err != nil {
		t.Fatalf("opaque whiteout on a missing directory: handled=%v err=%v", handled, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "sub", "victim"), []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	handled, err = applyWhiteout(root, "sub/.wh.victim")
	if !handled || err != nil {
		t.Fatalf("named whiteout: handled=%v err=%v", handled, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "sub", "victim")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("named whiteout did not remove the victim: err=%v", statErr)
	}

	for _, name := range []string{"sub/.wh.", "sub/.wh..", "sub/.wh..."} {
		handled, err = applyWhiteout(root, name)
		if !handled || err == nil {
			t.Fatalf("invalid whiteout %q: handled=%v err=%v, want an error", name, handled, err)
		}
	}

	handled, err = applyWhiteout(root, "sub/regular-file")
	if handled || err != nil {
		t.Fatalf("non-whiteout path: handled=%v err=%v", handled, err)
	}
}

func TestNormalizedMode(t *testing.T) {
	if got := normalizedMode(0o600, true); got != 0o755 {
		t.Fatalf("directory mode=%o, want 0755 regardless of the requested mode", got)
	}
	if got := normalizedMode(0, false); got != 0o644 {
		t.Fatalf("zero file mode=%o, want the 0644 default", got)
	}
	if got := normalizedMode(0o100755, false); got != 0o755 {
		t.Fatalf("file mode=%o, want the permission bits only (0755)", got)
	}
}

func TestBudgetReaderEnforcesMaxBytes(t *testing.T) {
	source := strings.NewReader("hello world")
	budget := &extractionBudget{maxBytes: 5}
	reader := &budgetReader{source: source, budget: budget}

	buffer := make([]byte, 4)
	n, err := reader.Read(buffer)
	if err != nil || n != 4 {
		t.Fatalf("first read: n=%d err=%v", n, err)
	}

	n, err = reader.Read(buffer)
	if err == nil || !strings.Contains(err.Error(), "exceed MaxBytes") {
		t.Fatalf("second read: n=%d err=%v, want a MaxBytes error", n, err)
	}
}

func TestBudgetReaderRejectsFurtherReadsOnceExhausted(t *testing.T) {
	source := strings.NewReader("more data than allowed")
	budget := &extractionBudget{maxBytes: 4, bytes: 4}
	reader := &budgetReader{source: source, budget: budget}

	buffer := make([]byte, 4)
	if _, err := reader.Read(buffer); err == nil || !strings.Contains(err.Error(), "exceed MaxBytes") {
		t.Fatalf("read after exhaustion: err=%v, want a MaxBytes error", err)
	}
}

func TestBudgetReaderPropagatesUnderlyingEOFOnceExhausted(t *testing.T) {
	source := strings.NewReader("")
	budget := &extractionBudget{maxBytes: 4, bytes: 4}
	reader := &budgetReader{source: source, budget: budget}

	buffer := make([]byte, 4)
	n, err := reader.Read(buffer)
	if n != 0 || err == nil || strings.Contains(err.Error(), "exceed MaxBytes") {
		t.Fatalf("read at exact budget with no more data: n=%d err=%v, want a plain EOF", n, err)
	}
}

func readTestBlob(t *testing.T, root, digest string) []byte {
	t.Helper()
	data, err := os.ReadFile(testBlobPath(root, digest))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readTestJSON(t *testing.T, name string, target any) {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func writeTestJSON(t *testing.T, name string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

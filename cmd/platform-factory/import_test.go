package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunContainerAutomaticallyImportsLocalLayout(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	loaded := false
	runCalled := false
	execute := func(name string, args []string, stdin io.Reader, _, _ io.Writer) error {
		if name != "podman" {
			t.Fatalf("runtime=%s", name)
		}
		switch args[0] {
		case "image":
			if loaded {
				return nil
			}
			return errors.New("image not found")
		case "load":
			entries := map[string]bool{}
			archive := tar.NewReader(stdin)
			for {
				header, err := archive.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				entries[header.Name] = true
				if _, err := io.Copy(io.Discard, archive); err != nil {
					t.Fatal(err)
				}
			}
			if !entries["oci-layout"] || !entries["index.json"] {
				t.Fatalf("archive entries=%v", entries)
			}
			loaded = true
			return nil
		case "run":
			runCalled = true
			if args[len(args)-1] != "example/service:v1" {
				t.Fatalf("run args=%v", args)
			}
			return nil
		default:
			t.Fatalf("unexpected args=%v", args)
			return nil
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runContainer([]string{"--runtime=podman", layoutName}, &stdout, &stderr, execute); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !loaded || !runCalled {
		t.Fatalf("loaded=%v runCalled=%v", loaded, runCalled)
	}
}

func TestImportHelpersRejectMalformedLayouts(t *testing.T) {
	root := t.TempDir()
	if _, err := selectManifest(root, ""); err == nil || !strings.Contains(err.Error(), "read index") {
		t.Fatalf("missing index err=%v", err)
	}
	writeIndex := func(value any) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "index.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeIndex(ociIndex{})
	if _, err := selectManifest(root, ""); err == nil || !strings.Contains(err.Error(), "no manifests") {
		t.Fatalf("empty index err=%v", err)
	}
	writeIndex(ociIndex{Manifests: []ociDescriptor{{Digest: "sha256:a"}, {Digest: "sha256:b"}}})
	if _, err := selectManifest(root, ""); err == nil || !strings.Contains(err.Error(), "multiple manifests") {
		t.Fatalf("ambiguous index err=%v", err)
	}
	if _, err := selectManifest(root, "missing:v1"); err == nil || !strings.Contains(err.Error(), "no manifest") {
		t.Fatalf("missing reference err=%v", err)
	}
	if err := readLayoutJSON(root, "md5:invalid", &ociManifest{}); err == nil {
		t.Fatal("invalid digest accepted")
	}
	var archive bytes.Buffer
	if err := copyBlobToArchive(tar.NewWriter(&archive), root, "bad", "blob"); err == nil {
		t.Fatal("invalid blob digest accepted")
	}
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- writeLayoutArchive(writer, root) }()
	_, _ = io.ReadAll(reader)
	_ = reader.Close()
	if err := <-done; err == nil {
		t.Fatal("archive without blob directory accepted")
	}
}

func TestPrepareContainerImageSkipsImportWhenPresent(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	var calls [][]string
	execute := func(name string, args []string, _ io.Reader, _, _ io.Writer) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	image, err := prepareContainerImage("docker", "example/service:v1", layoutName, io.Discard, execute)
	if err != nil {
		t.Fatal(err)
	}
	if image != "example/service:v1" {
		t.Fatalf("image=%s", image)
	}
	if len(calls) != 1 || strings.Join(calls[0], " ") != "docker image inspect example/service:v1" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestRunImportLoadsVerifiedLayoutWithoutStartingContainer(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	loaded := false
	execute := func(name string, args []string, stdin io.Reader, _, _ io.Writer) error {
		if name != "docker" {
			t.Fatalf("runtime=%s", name)
		}
		switch args[0] {
		case "image":
			if loaded {
				return nil
			}
			return errors.New("not loaded")
		case "load":
			archive := tar.NewReader(stdin)
			for {
				header, err := archive.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if header.Name == "manifest.json" {
					loaded = true
				}
			}
			return nil
		default:
			t.Fatalf("import started an unexpected operation: %v", args)
			return nil
		}
	}
	var stdout, stderr bytes.Buffer
	code := runImport([]string{"--runtime=docker", "--layout", layoutName, "example/service:v1"}, &stdout, &stderr, execute)
	if code != 0 || !loaded || strings.TrimSpace(stdout.String()) != "example/service:v1" {
		t.Fatalf("code=%d loaded=%t stdout=%q stderr=%q", code, loaded, stdout.String(), stderr.String())
	}
}

func TestRunImportValidatesArguments(t *testing.T) {
	for _, args := range [][]string{{}, {"--runtime=other", "--layout=/tmp/x", "image"}, {"--layout=/tmp/x"}} {
		if code := runImport(args, io.Discard, io.Discard, nil); code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
}

func TestPrepareContainerImageRejectsInvalidOrMismatchedLayout(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	if _, err := prepareContainerImage(
		"podman", "other/service:v1", layoutName, io.Discard, nil,
	); err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("mismatch error=%v", err)
	}
	invalid := t.TempDir()
	if _, err := prepareContainerImage("podman", invalid, "", io.Discard, nil); err == nil {
		t.Fatal("invalid layout accepted")
	}
}

func TestWriteDockerArchiveProducesDockerSaveFormat(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- writeDockerArchive(writer, layoutName, "example/service:v1") }()

	entries := map[string][]byte{}
	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(archive)
		if err != nil {
			t.Fatal(err)
		}
		entries[header.Name] = data
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	manifestData, ok := entries["manifest.json"]
	if !ok {
		t.Fatalf("no manifest.json in archive: %v", keysOf(entries))
	}
	var manifest []dockerManifestEntry
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest) != 1 || len(manifest[0].RepoTags) != 1 || manifest[0].RepoTags[0] != "example/service:v1" {
		t.Fatalf("manifest=%+v", manifest)
	}
	if _, ok := entries[manifest[0].Config]; !ok {
		t.Fatalf("config blob %q missing", manifest[0].Config)
	}
	if len(manifest[0].Layers) == 0 {
		t.Fatal("no layers referenced")
	}
	for _, layer := range manifest[0].Layers {
		if _, ok := entries[layer]; !ok {
			t.Fatalf("layer blob %q missing", layer)
		}
	}
	// The config blob must be a valid OCI/docker image config.
	var config struct {
		RootFS struct {
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}
	if err := json.Unmarshal(entries[manifest[0].Config], &config); err != nil {
		t.Fatal(err)
	}
	if len(config.RootFS.DiffIDs) != len(manifest[0].Layers) {
		t.Fatalf("diff_ids=%d layers=%d", len(config.RootFS.DiffIDs), len(manifest[0].Layers))
	}
}

func TestSelectManifestRequiresKnownReference(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	if _, err := selectManifest(layoutName, "example/service:v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := selectManifest(layoutName, "unknown/image:tag"); err == nil {
		t.Fatal("unknown reference accepted")
	}
}

func TestSelectManifestSinglePlatformDefault(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	descriptor, err := selectManifest(layoutName, "")
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Digest == "" {
		t.Fatalf("descriptor=%+v", descriptor)
	}
}

func TestBlobPathRejectsMalformedDigest(t *testing.T) {
	for _, digest := range []string{"nothex", "sha256:short", "sha256:../escape", "plainstring"} {
		if _, err := blobPath("/root", digest); err == nil {
			t.Fatalf("accepted malformed digest %q", digest)
		}
	}
	good := "sha256:" + strings.Repeat("a", 64)
	if _, err := blobPath("/root", good); err != nil {
		t.Fatalf("rejected valid digest: %v", err)
	}
}

func TestWriteDockerArchiveErrors(t *testing.T) {
	// A layout without the requested reference fails before writing.
	layoutName := buildPublishLayout(t, "example/service", "v1")
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- writeDockerArchive(writer, layoutName, "missing/image:tag") }()
	_, _ = io.Copy(io.Discard, reader)
	if err := <-done; err == nil {
		t.Fatal("missing reference accepted")
	}

	// A layout whose manifest blob was removed fails when read.
	broken := buildPublishLayout(t, "example/broken", "v1")
	descriptor, err := selectManifest(broken, "example/broken:v1")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := blobPath(broken, descriptor.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blob); err != nil {
		t.Fatal(err)
	}
	reader, writer = io.Pipe()
	done = make(chan error, 1)
	go func() { done <- writeDockerArchive(writer, broken, "example/broken:v1") }()
	_, _ = io.Copy(io.Discard, reader)
	if err := <-done; err == nil {
		t.Fatal("missing manifest blob accepted")
	}
}

func TestStreamLayoutToRuntimeSelectsDockerFormat(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	var loaded []byte
	execute := func(name string, args []string, stdin io.Reader, _, _ io.Writer) error {
		if name != "docker" || args[0] != "load" {
			t.Fatalf("name=%s args=%v", name, args)
		}
		loaded, _ = io.ReadAll(stdin)
		return nil
	}
	if err := streamLayoutToRuntime("docker", layoutName, "example/service:v1", io.Discard, execute); err != nil {
		t.Fatal(err)
	}
	entries := map[string]bool{}
	archive := tar.NewReader(bytes.NewReader(loaded))
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		entries[header.Name] = true
		_, _ = io.Copy(io.Discard, archive)
	}
	if !entries["manifest.json"] {
		t.Fatalf("docker stream missing manifest.json: %v", keysOf2(entries))
	}
}

func TestStreamLayoutToRuntimeSurfacesRuntimeError(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	failing := func(string, []string, io.Reader, io.Writer, io.Writer) error {
		return errors.New("load failed")
	}
	if err := streamLayoutToRuntime("docker", layoutName, "example/service:v1", io.Discard, failing); err == nil {
		t.Fatal("runtime error not surfaced")
	}
	if err := streamLayoutToRuntime("podman", layoutName, "example/service:v1", io.Discard, failing); err == nil {
		t.Fatal("podman runtime error not surfaced")
	}
}

func TestCopyBlobToArchiveRejectsNonRegularBlob(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	// Point at the blobs directory itself, which is not a regular file.
	dirDigest := "sha256:" + strings.Repeat("b", 64)
	if err := os.MkdirAll(filepath.Join(layoutName, "blobs", "sha256", strings.Repeat("b", 64)), 0o755); err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		tw := tar.NewWriter(writer)
		err := copyBlobToArchive(tw, layoutName, dirDigest, "blobs/sha256/x")
		_ = tw.Close()
		_ = writer.Close()
		done <- err
	}()
	_, _ = io.Copy(io.Discard, reader)
	if err := <-done; err == nil {
		t.Fatal("directory blob accepted")
	}
}

func keysOf2(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

func keysOf(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

func TestWriteLayoutArchiveRejectsUnsafeBlob(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	unsafe := filepath.Join(layoutName, "blobs", "sha256", "unsafe")
	if err := os.Symlink(filepath.Join(layoutName, "index.json"), unsafe); err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, _ = io.Copy(io.Discard, reader)
		done <- reader.Close()
	}()
	if err := writeLayoutArchive(writer, layoutName); err == nil {
		t.Fatal("unsafe blob accepted")
	}
	<-done
}

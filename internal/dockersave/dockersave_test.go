package dockersave

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/CYPT71/platform-factory/oci"
)

// fakeEngine is a minimal docker/podman-Engine-API-compatible HTTP server
// over a real Unix domain socket, standing in for a live daemon: it
// implements exactly the two endpoints PrepareContainerImage/
// streamLayoutToRuntime drive (POST /images/load, GET
// /images/{name}/json), the same "httptest.Server ... on a Unix socket"
// double the task's own verification guidance calls for.
type fakeEngine struct {
	mu       sync.Mutex
	loaded   map[string]bool
	lastLoad []byte
	loadErr  string // non-empty: /images/load succeeds (HTTP 200) but streams this as a load-time error
	failLoad bool   // true: /images/load itself returns a non-2xx status
}

func newFakeEngineSocket(t *testing.T) (*fakeEngine, string) {
	t.Helper()
	engine := &fakeEngine{loaded: map[string]bool{}}
	// A short, test-name-independent temp directory: t.TempDir() embeds
	// the (potentially long) test name in its path, which routinely
	// overflows the ~104-byte AF_UNIX path length limit once combined
	// with "/engine.sock" - a real, observed failure mode here, not a
	// theoretical one.
	dir, err := os.MkdirTemp("", "pfsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "e.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: engine}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return engine, socketPath
}

func (e *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/images/load":
		data, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		e.mu.Lock()
		e.lastLoad = data
		if e.failLoad {
			e.mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"load rejected"}`))
			return
		}
		if e.loadErr != "" {
			msg := e.loadErr
			e.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"stream":"Loading\n"}`))
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":%q}`, msg)))
			return
		}
		// Any archive containing a manifest.json (docker save) or
		// oci-layout (podman) marks every RepoTag/reference it can find as
		// loaded - good enough for these tests without a real image store.
		markLoadedFromArchive(data, e.loaded)
		e.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"stream":"Loaded image\n"}`))
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json") && strings.HasPrefix(r.URL.Path, "/images/"):
		reference := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/images/"), "/json")
		e.mu.Lock()
		exists := e.loaded[reference]
		e.mu.Unlock()
		if exists {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"no such image"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// markLoadedFromArchive scans a docker-save or OCI-layout tar archive for
// a RepoTag (docker) or an image ref annotation (podman) and marks it
// present - a light stand-in for a real store, sufficient for this
// package's own round-trip tests.
func markLoadedFromArchive(data []byte, loaded map[string]bool) {
	archive := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			return
		}
		if header.Name != "manifest.json" {
			continue
		}
		content, err := io.ReadAll(archive)
		if err != nil {
			return
		}
		var manifest []dockerManifestEntry
		if json.Unmarshal(content, &manifest) != nil {
			continue
		}
		for _, entry := range manifest {
			for _, tag := range entry.RepoTags {
				loaded[tag] = true
			}
		}
	}
}

// markPodmanLoaded is a test-only helper: since a raw OCI Image Layout
// archive (podman's format) has no top-level RepoTags list the way the
// Docker Save format does, tests that exercise the podman path mark the
// reference present explicitly through this hook instead of re-deriving
// it from index.json annotations.
func (e *fakeEngine) markLoaded(reference string) {
	e.mu.Lock()
	e.loaded[reference] = true
	e.mu.Unlock()
}

// buildLayout writes a minimal deterministic OCI Image Layout (via
// oci.Build) to exercise dockersave against real layout bytes,
// the same fixture cmd/platform-factory's own tests build.
func buildLayout(t *testing.T, image, tag string) string {
	t.Helper()
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "layout")
	if _, err := oci.Build(oci.Options{
		Binary: binary, Output: output, ImageName: image, Tag: tag,
	}); err != nil {
		t.Fatal(err)
	}
	return output
}

func keysOfBytes(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

func keysOfBool(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
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

func TestPrepareContainerImageAlwaysReimportsEvenWhenATagAlreadyExists(t *testing.T) {
	// A tag existing locally must never short-circuit the import:
	// docker/podman's own "image exists"/"image inspect" only check the
	// name, not content, so skipping the load whenever the tag is
	// already present would keep serving a stale image forever after
	// any rebuild - exactly the bug pf run's rebuild-on-change and
	// --watch exist to avoid. The fake engine reports the tag as already
	// present from the very first call (markLoaded, before any load), and
	// this still expects a real POST /images/load to happen.
	layoutName := buildLayout(t, "example/service", "v1")
	engine, socketPath := newFakeEngineSocket(t)
	engine.markLoaded("example/service:v1")
	client := NewSocketClient(socketPath)
	image, err := PrepareContainerImage(context.Background(), "docker", "example/service:v1", layoutName, io.Discard, client)
	if err != nil {
		t.Fatal(err)
	}
	if image != "example/service:v1" {
		t.Fatalf("image=%s", image)
	}
	engine.mu.Lock()
	loaded := engine.lastLoad
	engine.mu.Unlock()
	if len(loaded) == 0 {
		t.Fatal("expected the layout to actually be POSTed to /images/load, not skipped")
	}
}

func TestPrepareContainerImageLoadsALayoutContainingASecretShapedBinary(t *testing.T) {
	// pf import/pf run load a layout into the LOCAL runtime and never
	// push it anywhere, so unlike pf publish they must not be blocked by
	// layout.Verify's embedded-secret-marker scan self-flagging a binary
	// that (like platform-factory's own) happens to contain a
	// "password="-shaped string in its own compiled rodata.
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("password=hunter2"), 0o755); err != nil {
		t.Fatal(err)
	}
	layoutName := filepath.Join(root, "layout")
	if _, err := oci.Build(oci.Options{
		Binary: binary, Output: layoutName, ImageName: "example/service", Tag: "v1",
	}); err != nil {
		t.Fatal(err)
	}
	engine, socketPath := newFakeEngineSocket(t)
	engine.markLoaded("example/service:v1")
	client := NewSocketClient(socketPath)
	if _, err := PrepareContainerImage(context.Background(), "podman", "example/service:v1", layoutName, io.Discard, client); err != nil {
		t.Fatalf("expected a secret-shaped local binary to still import: %v", err)
	}
	engine.mu.Lock()
	loaded := engine.lastLoad
	engine.mu.Unlock()
	if len(loaded) == 0 {
		t.Fatal("expected the layout to actually be loaded")
	}
}

func TestPrepareContainerImageRejectsInvalidOrMismatchedLayout(t *testing.T) {
	layoutName := buildLayout(t, "example/service", "v1")
	if _, err := PrepareContainerImage(
		context.Background(), "podman", "other/service:v1", layoutName, io.Discard, nil,
	); err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("mismatch error=%v", err)
	}
	invalid := t.TempDir()
	if _, err := PrepareContainerImage(context.Background(), "podman", invalid, "", io.Discard, nil); err == nil {
		t.Fatal("invalid layout accepted")
	}
}

func TestWriteDockerArchiveProducesDockerSaveFormat(t *testing.T) {
	layoutName := buildLayout(t, "example/service", "v1")
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- WriteDockerArchive(writer, layoutName, "example/service:v1") }()

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
		t.Fatalf("no manifest.json in archive: %v", keysOfBytes(entries))
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
	layoutName := buildLayout(t, "example/service", "v1")
	if _, err := selectManifest(layoutName, "example/service:v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := selectManifest(layoutName, "unknown/image:tag"); err == nil {
		t.Fatal("unknown reference accepted")
	}
}

func TestSelectManifestSinglePlatformDefault(t *testing.T) {
	layoutName := buildLayout(t, "example/service", "v1")
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
	layoutName := buildLayout(t, "example/service", "v1")
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- WriteDockerArchive(writer, layoutName, "missing/image:tag") }()
	_, _ = io.Copy(io.Discard, reader)
	if err := <-done; err == nil {
		t.Fatal("missing reference accepted")
	}

	// A layout whose manifest blob was removed fails when read.
	broken := buildLayout(t, "example/broken", "v1")
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
	go func() { done <- WriteDockerArchive(writer, broken, "example/broken:v1") }()
	_, _ = io.Copy(io.Discard, reader)
	if err := <-done; err == nil {
		t.Fatal("missing manifest blob accepted")
	}
}

// stubRuntimeClient is a minimal RuntimeClient test double: LoadArchive
// delegates to load (nil means "succeed, discarding the body"), and
// ImageExists is never exercised by the streamLayoutToRuntime-focused
// tests below.
type stubRuntimeClient struct {
	load func(ctx context.Context, body io.Reader) error
}

func (s stubRuntimeClient) LoadArchive(ctx context.Context, body io.Reader) error {
	if s.load != nil {
		return s.load(ctx, body)
	}
	_, err := io.Copy(io.Discard, body)
	return err
}

func (s stubRuntimeClient) ImageExists(context.Context, string) (bool, error) {
	return false, errors.New("ImageExists not expected in this test")
}

func TestStreamLayoutToRuntimeSelectsDockerFormat(t *testing.T) {
	layoutName := buildLayout(t, "example/service", "v1")
	var loaded []byte
	client := stubRuntimeClient{load: func(_ context.Context, body io.Reader) error {
		data, err := io.ReadAll(body)
		loaded = data
		return err
	}}
	if err := streamLayoutToRuntime(context.Background(), "docker", layoutName, "example/service:v1", io.Discard, client); err != nil {
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
		t.Fatalf("docker stream missing manifest.json: %v", keysOfBool(entries))
	}
}

func TestStreamLayoutToRuntimeSurfacesRuntimeError(t *testing.T) {
	layoutName := buildLayout(t, "example/service", "v1")
	failing := stubRuntimeClient{load: func(_ context.Context, body io.Reader) error {
		_, _ = io.Copy(io.Discard, body)
		return errors.New("load failed")
	}}
	if err := streamLayoutToRuntime(context.Background(), "docker", layoutName, "example/service:v1", io.Discard, failing); err == nil {
		t.Fatal("runtime error not surfaced")
	}
	if err := streamLayoutToRuntime(context.Background(), "podman", layoutName, "example/service:v1", io.Discard, failing); err == nil {
		t.Fatal("podman runtime error not surfaced")
	}
}

func TestCopyBlobToArchiveRejectsNonRegularBlob(t *testing.T) {
	layoutName := buildLayout(t, "example/service", "v1")
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

func TestWriteLayoutArchiveRejectsUnsafeBlob(t *testing.T) {
	layoutName := buildLayout(t, "example/service", "v1")
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

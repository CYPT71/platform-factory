package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
// over a real Unix domain socket: since prepareContainerImage now talks
// to that socket directly (see internal/dockersave.SocketClient) instead
// of shelling to a CLI binary, these tests point $DOCKER_HOST/
// $CONTAINER_HOST at one instead of injecting a containerExecutor stub.
type fakeEngine struct {
	mu     sync.Mutex
	loaded map[string]bool
}

// pointBothRuntimeSocketsAtFakeEngine starts one fake engine and points
// both $DOCKER_HOST and $CONTAINER_HOST at it, so any test driving
// prepareContainerImage/PrepareContainerImage - regardless of which
// runtime name it exercises - resolves to a working socket instead of a
// real (absent, in this test environment) docker/podman daemon.
func pointBothRuntimeSocketsAtFakeEngine(t *testing.T) *fakeEngine {
	t.Helper()
	engine, socketPath := newFakeEngineSocket(t)
	t.Setenv("DOCKER_HOST", "unix://"+socketPath)
	t.Setenv("CONTAINER_HOST", "unix://"+socketPath)
	return engine
}

func newFakeEngineSocket(t *testing.T) (*fakeEngine, string) {
	t.Helper()
	engine := &fakeEngine{loaded: map[string]bool{}}
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
		markLoadedFromTestArchive(data, e.loaded)
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

// markLoadedFromTestArchive marks a reference present after a load: a
// docker-save archive's manifest.json names its RepoTags directly; an OCI
// Image Layout archive (podman's format) has none, so index.json's own
// image-ref annotation is used instead - the same field
// internal/dockersave.selectManifest reads.
func markLoadedFromTestArchive(data []byte, loaded map[string]bool) {
	archive := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			return
		}
		content, err := io.ReadAll(archive)
		if err != nil {
			return
		}
		switch header.Name {
		case "manifest.json":
			var manifest []struct {
				RepoTags []string `json:"RepoTags"`
			}
			if json.Unmarshal(content, &manifest) != nil {
				continue
			}
			for _, entry := range manifest {
				for _, tag := range entry.RepoTags {
					loaded[tag] = true
				}
			}
		case "index.json":
			var index struct {
				Manifests []struct {
					Annotations map[string]string `json:"annotations"`
				} `json:"manifests"`
			}
			if json.Unmarshal(content, &index) != nil {
				continue
			}
			for _, manifest := range index.Manifests {
				if ref := manifest.Annotations["org.opencontainers.image.ref.name"]; ref != "" {
					loaded[ref] = true
				}
			}
		}
	}
}

func TestRunContainerAutomaticallyImportsLocalLayout(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	_, socketPath := newFakeEngineSocket(t)
	t.Setenv("CONTAINER_HOST", "unix://"+socketPath)
	runCalled := false
	execute := func(name string, args []string, _ io.Reader, _, _ io.Writer) error {
		if name != "podman" || args[0] != "run" {
			t.Fatalf("unexpected exec name=%s args=%v", name, args)
		}
		runCalled = true
		if args[len(args)-1] != "example/service:v1" {
			t.Fatalf("run args=%v", args)
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := runContainer(context.Background(), []string{"--runtime=podman", layoutName}, &stdout, &stderr, execute); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !runCalled {
		t.Fatal("expected podman run to be invoked after the local layout was loaded over the socket")
	}
}

func TestPrepareContainerImageAlwaysReimportsEvenWhenATagAlreadyExists(t *testing.T) {
	// A tag existing locally must never short-circuit the import:
	// docker/podman's own "image exists"/"image inspect" only check the
	// name, not content, so skipping the load whenever the tag is
	// already present would keep serving a stale image forever after
	// any rebuild - exactly the bug pf run's rebuild-on-change and
	// --watch exist to avoid. The fake engine reports the tag as already
	// present from the very first call, and this still expects a real
	// POST /images/load to happen.
	layoutName := buildPublishLayout(t, "example/service", "v1")
	engine, socketPath := newFakeEngineSocket(t)
	t.Setenv("DOCKER_HOST", "unix://"+socketPath)
	engine.mu.Lock()
	engine.loaded["example/service:v1"] = true
	engine.mu.Unlock()
	image, err := prepareContainerImage(context.Background(), "docker", "example/service:v1", layoutName, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if image != "example/service:v1" {
		t.Fatalf("image=%s", image)
	}
}

func TestRunImportLoadsVerifiedLayoutWithoutStartingContainer(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	_, socketPath := newFakeEngineSocket(t)
	t.Setenv("DOCKER_HOST", "unix://"+socketPath)
	var stdout, stderr bytes.Buffer
	code := runImport(context.Background(), []string{"--runtime=docker", "--layout", layoutName, "example/service:v1"}, &stdout, &stderr)
	if code != 0 || strings.TrimSpace(stdout.String()) != "example/service:v1" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunImportValidatesArguments(t *testing.T) {
	for _, args := range [][]string{{}, {"--runtime=other", "--layout=/tmp/x", "image"}, {"--layout=/tmp/x"}} {
		if code := runImport(context.Background(), args, io.Discard, io.Discard); code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
}

func TestRunImportSurfacesSocketFailure(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/docker.sock")
	var stdout, stderr bytes.Buffer
	code := runImport(context.Background(), []string{"--runtime=docker", "--layout", layoutName, "example/service:v1"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "platform-factory import:") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
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
	_, socketPath := newFakeEngineSocket(t)
	t.Setenv("CONTAINER_HOST", "unix://"+socketPath)
	if _, err := prepareContainerImage(context.Background(), "podman", "example/service:v1", layoutName, io.Discard); err != nil {
		t.Fatalf("expected a secret-shaped local binary to still import: %v", err)
	}
}

func TestPrepareContainerImageRejectsInvalidOrMismatchedLayout(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	if _, err := prepareContainerImage(
		context.Background(), "podman", "other/service:v1", layoutName, io.Discard,
	); err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("mismatch error=%v", err)
	}
	invalid := t.TempDir()
	if _, err := prepareContainerImage(context.Background(), "podman", invalid, "", io.Discard); err == nil {
		t.Fatal("invalid layout accepted")
	}
}

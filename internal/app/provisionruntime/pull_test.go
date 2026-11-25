package provisionruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/registry"
)

type imagePullRoundTripFunc func(*http.Request) *http.Response

func (f imagePullRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request), nil
}

func sha256Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// buildFakeSingleLayerImage assembles a minimal, real (gzip'd tar) OCI
// image - one layer containing one file - and returns every blob and
// digest a fake registry needs to serve it, so pullImageRootfsWithClient
// can be exercised against real bytes without a network call.
func buildFakeSingleLayerImage(t *testing.T) (manifestDigest string, manifestBytes, configBytes, layerBytes []byte, layerDigest, configDigest string) {
	t.Helper()
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	content := []byte("hello from a fake pulled image\n")
	if err := tw.WriteHeader(&tar.Header{Name: "hello.txt", Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	diffID := sha256Digest(tarBuf.Bytes())

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	if _, err := gw.Write(tarBuf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	layerBytes = gzBuf.Bytes()
	layerDigest = sha256Digest(layerBytes)

	configBytes, err := json.Marshal(map[string]any{
		"os": "linux", "architecture": "amd64",
		"rootfs": map[string]any{"type": "layers", "diff_ids": []string{diffID}},
		"config": map[string]any{"Cmd": []string{"python3"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	configDigest = sha256Digest(configBytes)

	manifestBytes, err = json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config":        map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": configDigest, "size": len(configBytes)},
		"layers": []map[string]any{{
			"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": layerDigest, "size": len(layerBytes),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest = sha256Digest(manifestBytes)
	return manifestDigest, manifestBytes, configBytes, layerBytes, layerDigest, configDigest
}

func TestPullImageRootfsWithClientAgainstAFakeRegistry(t *testing.T) {
	manifestDigest, manifestBytes, configBytes, layerBytes, layerDigest, configDigest := buildFakeSingleLayerImage(t)

	const repository = "registry.example/team/image"
	transport := imagePullRoundTripFunc(func(request *http.Request) *http.Response {
		path := request.URL.Path
		var body []byte
		switch {
		case strings.HasSuffix(path, "/manifests/"+manifestDigest):
			body = manifestBytes
		case strings.HasSuffix(path, "/blobs/"+configDigest):
			body = configBytes
		case strings.HasSuffix(path, "/blobs/"+layerDigest):
			body = layerBytes
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: http.Header{}}
		}
		return &http.Response{
			StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)), Header: http.Header{"Content-Type": {"application/vnd.oci.image.manifest.v1+json"}},
		}
	})
	client := &registry.Client{Scheme: "https", HTTP: &http.Client{Transport: transport}}

	dest := filepath.Join(t.TempDir(), "rootfs")
	resolved, err := pullImageRootfsWithClient(context.Background(), client, repository+"@"+manifestDigest, "amd64", dest)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != manifestDigest {
		t.Fatalf("resolved=%q want=%q", resolved, manifestDigest)
	}
	got, err := os.ReadFile(filepath.Join(dest, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello from a fake pulled image\n" {
		t.Fatalf("got=%q", got)
	}
}

func TestPullImageRootfsWithClientRejectsAMutatedLayer(t *testing.T) {
	manifestDigest, manifestBytes, configBytes, layerBytes, layerDigest, configDigest := buildFakeSingleLayerImage(t)
	tamperedLayer := append(append([]byte{}, layerBytes...), 0x00)

	const repository = "registry.example/team/image"
	transport := imagePullRoundTripFunc(func(request *http.Request) *http.Response {
		path := request.URL.Path
		var body []byte
		switch {
		case strings.HasSuffix(path, "/manifests/"+manifestDigest):
			body = manifestBytes
		case strings.HasSuffix(path, "/blobs/"+configDigest):
			body = configBytes
		case strings.HasSuffix(path, "/blobs/"+layerDigest):
			// A tampered/corrupted layer must still be rejected by
			// GetBlob's own digest check even though it was requested
			// under the correct digest - the same fail-closed posture
			// every other registry consumer in this codebase relies on.
			body = tamperedLayer
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: http.Header{}}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Header: http.Header{}}
	})
	client := &registry.Client{Scheme: "https", HTTP: &http.Client{Transport: transport}}

	dest := filepath.Join(t.TempDir(), "rootfs")
	if _, err := pullImageRootfsWithClient(context.Background(), client, repository+"@"+manifestDigest, "amd64", dest); err == nil {
		t.Fatal("expected a digest mismatch error for a tampered layer")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("a failed pull must not leave a partial rootfs behind: %v", err)
	}
}

func TestPullImageRootfsWithClientSelectsRequestedArchitectureFromAnIndex(t *testing.T) {
	amd64Digest, amd64Manifest, amd64Config, amd64Layer, amd64LayerDigest, amd64ConfigDigest := buildFakeSingleLayerImage(t)

	indexBytes, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{
			{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": amd64Digest, "size": len(amd64Manifest), "platform": map[string]string{"os": "linux", "architecture": "amd64"}},
			{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:" + strings.Repeat("a", 64), "size": 2, "platform": map[string]string{"os": "linux", "architecture": "arm64"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	indexDigest := sha256Digest(indexBytes)

	const repository = "registry.example/team/image"
	transport := imagePullRoundTripFunc(func(request *http.Request) *http.Response {
		path := request.URL.Path
		var body []byte
		switch {
		case strings.HasSuffix(path, "/manifests/"+indexDigest):
			body = indexBytes
		case strings.HasSuffix(path, "/manifests/"+amd64Digest):
			body = amd64Manifest
		case strings.HasSuffix(path, "/blobs/"+amd64ConfigDigest):
			body = amd64Config
		case strings.HasSuffix(path, "/blobs/"+amd64LayerDigest):
			body = amd64Layer
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: http.Header{}}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Header: http.Header{}}
	})
	client := &registry.Client{Scheme: "https", HTTP: &http.Client{Transport: transport}}

	dest := filepath.Join(t.TempDir(), "rootfs")
	resolved, err := pullImageRootfsWithClient(context.Background(), client, repository+"@"+indexDigest, "amd64", dest)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != amd64Digest {
		t.Fatalf("resolved=%q want the amd64 platform manifest %q, not the index digest itself", resolved, amd64Digest)
	}
	if _, err := os.Stat(filepath.Join(dest, "hello.txt")); err != nil {
		t.Fatalf("expected the amd64 platform's content to be extracted: %v", err)
	}

	if _, err := pullImageRootfsWithClient(context.Background(), client, repository+"@"+indexDigest, "riscv64", filepath.Join(t.TempDir(), "rootfs")); err == nil {
		t.Fatal("expected an error for a platform the index does not contain")
	} else if !strings.Contains(fmt.Sprint(err), "no manifest for linux/riscv64") {
		t.Fatalf("unexpected error for a missing platform: %v", err)
	}
}

// TestPullImageRootfsAgainstRealDockerHub exercises the real Docker Hub
// anonymous-pull bearer-auth flow and the real registry-1.docker.io API -
// deliberately not hermetic, since that auth flow has never been
// exercised against this client before and needs proving against the
// real service at least once. Skipped unless
// PLATFORM_FACTORY_TEST_LIVE_REGISTRY=1, matching this repo's existing
// convention for opt-in real-infrastructure tests (see
// PLATFORM_FACTORY_TEST_BZIMAGE in internal/hypervisor/kvm).
// TestPullImageRootfsRejectsAnInvalidReferenceBeforeAnyNetworkCall
// exercises pullImageRootfs itself (not pullImageRootfsWithClient's fake-
// registry variants above) - reference parsing fails before the real
// *registry.Client it constructs ever makes a request, so this needs no
// network and never runs against the fake or real registry.
func TestPullImageRootfsRejectsAnInvalidReferenceBeforeAnyNetworkCall(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "rootfs")
	if _, err := pullImageRootfs(context.Background(), "not a valid reference", "amd64", dest); err == nil {
		t.Fatal("expected an error for an unparseable image reference")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("destination should not have been created: err=%v", err)
	}
}

func TestPullImageRootfsWithCacheReusesVerifiedBlobsAndRecoversCorruption(t *testing.T) {
	manifestDigest, manifestBytes, configBytes, layerBytes, layerDigest, configDigest := buildFakeSingleLayerImage(t)
	const repository = "registry.example/team/image"

	requests := 0
	transport := imagePullRoundTripFunc(func(request *http.Request) *http.Response {
		requests++
		path := request.URL.Path
		var body []byte
		switch {
		case strings.HasSuffix(path, "/manifests/"+manifestDigest):
			body = manifestBytes
		case strings.HasSuffix(path, "/blobs/"+configDigest):
			body = configBytes
		case strings.HasSuffix(path, "/blobs/"+layerDigest):
			body = layerBytes
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: http.Header{}}
		}
		return &http.Response{
			StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)), Header: http.Header{},
		}
	})
	client := &registry.Client{Scheme: "https", HTTP: &http.Client{Transport: transport}}
	cacheDir := filepath.Join(t.TempDir(), "cache")

	// First pull populates the cache.
	if _, err := pullImageRootfsWithCache(context.Background(), client, repository+"@"+manifestDigest, "amd64", filepath.Join(t.TempDir(), "r1"), cacheDir); err != nil {
		t.Fatalf("first pull: %v", err)
	}
	if requests == 0 {
		t.Fatal("expected first pull to fetch from registry")
	}

	// Second pull must succeed with zero HTTP requests (offline cache reuse).
	offlineTransport := imagePullRoundTripFunc(func(request *http.Request) *http.Response {
		t.Fatalf("unexpected network call on cached pull: %s", request.URL.Path)
		return nil
	})
	offlineClient := &registry.Client{Scheme: "https", HTTP: &http.Client{Transport: offlineTransport}}
	dest2 := filepath.Join(t.TempDir(), "r2")
	if _, err := pullImageRootfsWithCache(context.Background(), offlineClient, repository+"@"+manifestDigest, "amd64", dest2, cacheDir); err != nil {
		t.Fatalf("cached pull: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dest2, "hello.txt")); err != nil || string(got) != "hello from a fake pulled image\n" {
		t.Fatalf("cached rootfs content=%q err=%v", got, err)
	}

	// Corrupt a cached blob; cache reader must reject it and re-fetch cleanly.
	hexDigest := strings.TrimPrefix(layerDigest, "sha256:")
	cachedLayer := filepath.Join(cacheDir, "blobs", "sha256", hexDigest)
	if err := os.Chmod(cachedLayer, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachedLayer, []byte("corrupted"), 0o444); err != nil {
		t.Fatal(err)
	}
	before := requests
	if _, err := pullImageRootfsWithCache(context.Background(), client, repository+"@"+manifestDigest, "amd64", filepath.Join(t.TempDir(), "r3"), cacheDir); err != nil {
		t.Fatalf("recovery pull after cache corruption: %v", err)
	}
	if requests <= before {
		t.Fatal("expected corrupted cache entry to trigger re-fetch")
	}
}

func TestPullImageRootfsRejectsMismatchedSinglePlatform(t *testing.T) {
	manifestDigest, manifestBytes, configBytes, layerBytes, layerDigest, configDigest := buildFakeSingleLayerImage(t)
	const repository = "registry.example/team/image"
	transport := imagePullRoundTripFunc(func(request *http.Request) *http.Response {
		path := request.URL.Path
		var body []byte
		switch {
		case strings.HasSuffix(path, "/manifests/"+manifestDigest):
			body = manifestBytes
		case strings.HasSuffix(path, "/blobs/"+configDigest):
			body = configBytes
		case strings.HasSuffix(path, "/blobs/"+layerDigest):
			body = layerBytes
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: http.Header{}}
		}
		return &http.Response{
			StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)), Header: http.Header{},
		}
	})
	client := &registry.Client{Scheme: "https", HTTP: &http.Client{Transport: transport}}
	_, err := pullImageRootfsWithClient(context.Background(), client, repository+"@"+manifestDigest, "arm64", filepath.Join(t.TempDir(), "rootfs"))
	if err == nil || !strings.Contains(err.Error(), "targets linux/amd64, expected linux/arm64") {
		t.Fatalf("expected arch mismatch error, got %v", err)
	}
}

func TestDefaultRuntimeCacheDirHonorsOverridesInPrecedenceOrder(t *testing.T) {
	t.Setenv("PLATFORM_FACTORY_RUNTIME_CACHE_DIR", "")
	t.Setenv("PLATFORM_FACTORY_CACHE_DIR", "")
	if got := defaultRuntimeCacheDir(); got == "" {
		t.Fatal("expected a non-empty default cache dir derived from os.UserCacheDir")
	}

	t.Setenv("PLATFORM_FACTORY_CACHE_DIR", "/tmp/example-cache")
	if got, want := defaultRuntimeCacheDir(), filepath.Join("/tmp/example-cache", "runtime-oci"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	for _, off := range []string{"off", "OFF", "none", "None"} {
		t.Setenv("PLATFORM_FACTORY_RUNTIME_CACHE_DIR", off)
		if got := defaultRuntimeCacheDir(); got != "" {
			t.Fatalf("off=%q: got %q, want empty (disabled)", off, got)
		}
	}

	t.Setenv("PLATFORM_FACTORY_RUNTIME_CACHE_DIR", "/tmp/explicit-cache")
	if got := defaultRuntimeCacheDir(); got != "/tmp/explicit-cache" {
		t.Fatalf("got %q, want the explicit override unmodified", got)
	}
}

func TestReadVerifiedCacheRejectsDisabledCacheAndMalformedDigest(t *testing.T) {
	if data, ok := readVerifiedCache("", "sha256:"+strings.Repeat("a", 64)); ok || data != nil {
		t.Fatalf("expected no cache lookup when cacheDir is empty, got data=%v ok=%v", data, ok)
	}
	cacheDir := t.TempDir()
	for _, digest := range []string{"not-a-digest", "sha256:tooshort", "md5:" + strings.Repeat("a", 64)} {
		if data, ok := readVerifiedCache(cacheDir, digest); ok || data != nil {
			t.Fatalf("digest=%q: expected a malformed digest to be rejected, got data=%v ok=%v", digest, data, ok)
		}
	}
}

func TestWriteVerifiedCacheRejectsDisabledCacheMalformedDigestAndHashMismatch(t *testing.T) {
	// A disabled cache or a malformed digest must be silently ignored - no
	// panic, and definitely nothing written to disk.
	writeVerifiedCache("", "sha256:"+strings.Repeat("a", 64), []byte("payload"))

	cacheDir := t.TempDir()
	writeVerifiedCache(cacheDir, "not-a-digest", []byte("payload"))
	if entries, err := os.ReadDir(cacheDir); err != nil || len(entries) != 0 {
		t.Fatalf("expected nothing written for a malformed digest: entries=%v err=%v", entries, err)
	}

	// The digest must match the data's actual SHA-256, or nothing is written.
	wrongDigest := "sha256:" + strings.Repeat("b", 64)
	writeVerifiedCache(cacheDir, wrongDigest, []byte("payload"))
	if _, ok := readVerifiedCache(cacheDir, wrongDigest); ok {
		t.Fatal("expected a hash-mismatched write to be rejected")
	}

	// A correctly hashed write must round-trip through readVerifiedCache.
	data := []byte("real payload")
	sum := sha256.Sum256(data)
	validDigest := "sha256:" + hex.EncodeToString(sum[:])
	writeVerifiedCache(cacheDir, validDigest, data)
	got, ok := readVerifiedCache(cacheDir, validDigest)
	if !ok || string(got) != string(data) {
		t.Fatalf("got=%q ok=%v, want %q", got, ok, data)
	}
}

func TestPullImageRootfsAgainstRealDockerHub(t *testing.T) {
	if os.Getenv("PLATFORM_FACTORY_TEST_LIVE_REGISTRY") != "1" {
		t.Skip("set PLATFORM_FACTORY_TEST_LIVE_REGISTRY=1 to pull python:3.12-slim from the real Docker Hub")
	}
	dest := filepath.Join(t.TempDir(), "rootfs")
	digest, err := pullImageRootfs(context.Background(), "python@sha256:dd29372629eeba2dd003fd9e9d35a5b8236c44727875a0364254b5127af88e65", "amd64", dest)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("resolved platform manifest digest: %s", digest)
	interpreter := filepath.Join(dest, "usr", "local", "bin", "python3.12")
	if info, err := os.Stat(interpreter); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("expected an executable interpreter at %s: %v", interpreter, err)
	}
}

// TestPullImageRootfsAgainstRealGHCR proves this package's registry
// interop against a second real, independently-operated OCI registry -
// not just Docker Hub (TestPullImageRootfsAgainstRealDockerHub). GHCR's
// anonymous pull still requires a real bearer-token exchange
// (WWW-Authenticate challenge -> POST to the realm it names), unlike some
// registries that serve public blobs with no auth round-trip at all, so
// this exercises authorize/parseAuthParameters against a provider whose
// challenge response shape Docker Hub's own token service does not
// necessarily match byte-for-byte.
func TestPullImageRootfsAgainstRealGHCR(t *testing.T) {
	if os.Getenv("PLATFORM_FACTORY_TEST_LIVE_REGISTRY") != "1" {
		t.Skip("set PLATFORM_FACTORY_TEST_LIVE_REGISTRY=1 to pull a real public image from ghcr.io")
	}
	dest := filepath.Join(t.TempDir(), "rootfs")
	// ghcr.io/oras-project/oras is ORAS's own published CLI image - public,
	// small, and stable; this is the linux/amd64 manifest digest under tag
	// v1.2.0, resolved once by hand and pinned here the same way any real
	// caller would pin a third-party base image.
	digest, err := pullImageRootfs(context.Background(),
		"ghcr.io/oras-project/oras@sha256:42c524c48e0672568dbd2842d3a0cb34a415347145ee9fe1c8abaf65e7455b46",
		"amd64", dest)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("resolved platform manifest digest: %s", digest)
	binary := filepath.Join(dest, "bin", "oras")
	if info, err := os.Stat(binary); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("expected an executable oras binary at %s: %v", binary, err)
	}
}

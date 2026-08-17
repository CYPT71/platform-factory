package main

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

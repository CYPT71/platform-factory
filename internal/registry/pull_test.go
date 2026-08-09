package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

func TestGetManifestFetchesByTagAndVerifiesDigestFetch(t *testing.T) {
	payload := []byte(`{"schemaVersion":2}`)
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	target := Reference{Registry: "registry.example", Repository: "team/service"}

	var gotAccept, gotPath string
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		gotAccept = r.Header.Get("Accept")
		gotPath = r.URL.Path
		result := response(http.StatusOK, "", bytes.NewReader(payload))
		result.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		return result
	})}}

	body, contentType, err := client.GetManifest(context.Background(), target, "v1")
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("body=%s want %s", body, payload)
	}
	if contentType != "application/vnd.oci.image.manifest.v1+json" {
		t.Fatalf("content-type=%q", contentType)
	}
	if gotPath != "/v2/team/service/manifests/v1" {
		t.Fatalf("path=%q", gotPath)
	}
	if !strings.Contains(gotAccept, "application/vnd.oci.image.manifest.v1+json") {
		t.Fatalf("Accept header missing manifest media type: %q", gotAccept)
	}

	// Fetching by the digest itself must succeed only if the server's bytes
	// actually hash to that digest.
	body2, _, err := client.GetManifest(context.Background(), target, digest)
	if err != nil || !bytes.Equal(body2, payload) {
		t.Fatalf("GetManifest(by digest): body=%s err=%v", body2, err)
	}
}

func TestGetManifestRejectsDigestMismatch(t *testing.T) {
	target := Reference{Registry: "registry.example", Repository: "team/service"}
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		return response(http.StatusOK, "", strings.NewReader(`{"tampered":true}`))
	})}}

	wrongDigest := "sha256:" + strings.Repeat("0", 64)
	if _, _, err := client.GetManifest(context.Background(), target, wrongDigest); err == nil ||
		!strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected digest mismatch, got %v", err)
	}
}

func TestGetManifestFailsClosedOnHTTPError(t *testing.T) {
	target := Reference{Registry: "registry.example", Repository: "team/service"}
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		return response(http.StatusNotFound, "", strings.NewReader(`{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`))
	})}}
	if _, _, err := client.GetManifest(context.Background(), target, "missing"); err == nil ||
		!strings.Contains(err.Error(), "get manifest") {
		t.Fatalf("expected a get-manifest error, got %v", err)
	}
}

func TestGetBlobFetchesAndVerifiesDigest(t *testing.T) {
	payload := []byte("blob content")
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	target := Reference{Registry: "registry.example", Repository: "team/service"}

	var gotPath string
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		gotPath = r.URL.Path
		return response(http.StatusOK, "", bytes.NewReader(payload))
	})}}

	blob, err := client.GetBlob(context.Background(), target, digest)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	if !bytes.Equal(blob, payload) {
		t.Fatalf("blob=%s want %s", blob, payload)
	}
	if gotPath != "/v2/team/service/blobs/"+digest {
		t.Fatalf("path=%q", gotPath)
	}
}

func TestGetBlobRejectsDigestMismatch(t *testing.T) {
	target := Reference{Registry: "registry.example", Repository: "team/service"}
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		return response(http.StatusOK, "", strings.NewReader("not the right content"))
	})}}

	wantDigest := "sha256:" + strings.Repeat("a", 64)
	if _, err := client.GetBlob(context.Background(), target, wantDigest); err == nil ||
		!strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected digest mismatch, got %v", err)
	}
}

func TestGetBlobRejectsInvalidDigestBeforeAnyRequest(t *testing.T) {
	target := Reference{Registry: "registry.example", Repository: "team/service"}
	called := false
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		called = true
		return response(http.StatusOK, "", nil)
	})}}
	if _, err := client.GetBlob(context.Background(), target, "not-a-digest"); err == nil {
		t.Fatal("expected invalid digest to be rejected")
	}
	if called {
		t.Fatal("GetBlob should not make a request for a syntactically invalid digest")
	}
}

func TestGetBlobEnforcesFetchSizeLimit(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), maxFetchedBlobSize+1)
	sum := sha256.Sum256(oversized)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	target := Reference{Registry: "registry.example", Repository: "team/service"}
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		return response(http.StatusOK, "", bytes.NewReader(oversized))
	})}}
	if _, err := client.GetBlob(context.Background(), target, digest); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected a fetch-limit error, got %v", err)
	}
}

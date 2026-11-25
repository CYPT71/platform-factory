package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"
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

func TestGetManifestRetriesTransientErrorsAndRefreshesExpiredBearerToken(t *testing.T) {
	payload := []byte(`{"schemaVersion":2}`)
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	target := Reference{Registry: "registry.example", Repository: "team/service"}

	attempts := 0
	tokenFetches := 0
	client := &Client{
		Scheme:         "https",
		RetryBaseDelay: time.Millisecond,
		token:          "expired-token",
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
			if r.URL.Host == "auth.example" {
				tokenFetches++
				return response(http.StatusOK, "", strings.NewReader(`{"token":"fresh-token"}`))
			}
			attempts++
			if attempts == 1 {
				resp := response(http.StatusUnauthorized, "", strings.NewReader("expired"))
				resp.Header.Set("WWW-Authenticate", `Bearer realm="https://auth.example/token",service="registry.example",scope="repository:team/service:pull"`)
				return resp
			}
			if attempts == 2 {
				return response(http.StatusServiceUnavailable, "", strings.NewReader("try again"))
			}
			if r.Header.Get("Authorization") != "Bearer fresh-token" {
				return response(http.StatusUnauthorized, "", strings.NewReader("wrong token"))
			}
			return response(http.StatusOK, "", bytes.NewReader(payload))
		})},
	}

	got, _, err := client.GetManifest(context.Background(), target, digest)
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got=%s want %s", got, payload)
	}
	if tokenFetches != 1 || attempts != 3 {
		t.Fatalf("tokenFetches=%d attempts=%d, want 1 and 3", tokenFetches, attempts)
	}
}

func TestListReferrersNativeAndFallback(t *testing.T) {
	subject := "sha256:" + strings.Repeat("a", 64)
	artDigest := "sha256:" + strings.Repeat("b", 64)
	target := Reference{Registry: "registry.example", Repository: "team/service"}
	indexJSON := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","artifactType":"application/vnd.example.sig","digest":"` + artDigest + `","size":42}]}`

	// Native OCI 1.1 /referrers/<digest>
	nativeClient := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		if r.URL.Path == "/v2/team/service/referrers/"+subject {
			return response(http.StatusOK, "", strings.NewReader(indexJSON))
		}
		return response(http.StatusNotFound, "", strings.NewReader("not found"))
	})}}
	got, err := nativeClient.ListReferrers(context.Background(), target, subject)
	if err != nil || len(got) != 1 || got[0].Digest != artDigest || got[0].ArtifactType != "application/vnd.example.sig" {
		t.Fatalf("native referrers=%+v err=%v", got, err)
	}

	// Fallback tag sha256-<hex> when /referrers/ returns 404
	fallbackClient := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		if r.URL.Path == "/v2/team/service/manifests/sha256-"+strings.Repeat("a", 64) {
			return response(http.StatusOK, "", strings.NewReader(indexJSON))
		}
		return response(http.StatusNotFound, "", strings.NewReader("not found"))
	})}}
	got, err = fallbackClient.ListReferrers(context.Background(), target, subject)
	if err != nil || len(got) != 1 || got[0].Digest != artDigest {
		t.Fatalf("fallback referrers=%+v err=%v", got, err)
	}
}

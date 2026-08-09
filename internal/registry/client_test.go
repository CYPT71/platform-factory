package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/oci"
)

func testLayout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("registry payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "layout")
	if _, err := oci.Build(oci.Options{Binary: binary, Output: output, ImageName: "example/service", Tag: "v1"}); err != nil {
		t.Fatal(err)
	}
	return output
}

func TestReadVerifiedBlobRejectsCorruptDescriptorsAndContent(t *testing.T) {
	root := t.TempDir()
	blobDir := filepath.Join(root, "blobs", "sha256")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("verified manifest")
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(blobDir, strings.TrimPrefix(digest, "sha256:")), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := readVerifiedBlob(root, descriptor{Digest: digest, Size: int64(len(payload))}); err != nil ||
		!bytes.Equal(got, payload) {
		t.Fatalf("valid blob=%q err=%v", got, err)
	}
	cases := []struct {
		name string
		desc descriptor
	}{
		{name: "invalid digest", desc: descriptor{Digest: "md5:bad", Size: 1}},
		{name: "missing", desc: descriptor{Digest: "sha256:" + strings.Repeat("0", 64), Size: 1}},
		{name: "size", desc: descriptor{Digest: digest, Size: int64(len(payload) + 1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := readVerifiedBlob(root, tc.desc); err == nil {
				t.Fatal("invalid descriptor accepted")
			}
		})
	}
	corrupt := []byte("corrupt manifest!")
	if len(corrupt) != len(payload) {
		t.Fatalf("test payload lengths differ: %d != %d", len(corrupt), len(payload))
	}
	if err := os.WriteFile(filepath.Join(blobDir, strings.TrimPrefix(digest, "sha256:")), corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readVerifiedBlob(root, descriptor{Digest: digest, Size: int64(len(corrupt))}); err == nil ||
		!strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("corrupt manifest err=%v", err)
	}
}

func TestTagLayoutRejectsInvalidLayoutsBeforeRegistryAccess(t *testing.T) {
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
		t.Fatal("invalid layout reached registry")
		return nil
	})}}
	target := Reference{Registry: "registry.example", Repository: "team/service", Tag: "v1"}

	missing := t.TempDir()
	if err := client.TagLayout(context.Background(), missing, target, ""); err == nil ||
		!strings.Contains(err.Error(), "read layout index") {
		t.Fatalf("missing index err=%v", err)
	}
	invalid := t.TempDir()
	if err := os.WriteFile(filepath.Join(invalid, "index.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := client.TagLayout(context.Background(), invalid, target, ""); err == nil ||
		!strings.Contains(err.Error(), "decode layout index") {
		t.Fatalf("invalid index err=%v", err)
	}
	ambiguous := t.TempDir()
	index := imageIndex{Manifests: []descriptor{{Digest: "sha256:" + strings.Repeat("1", 64)}, {Digest: "sha256:" + strings.Repeat("2", 64)}}}
	encoded, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ambiguous, "index.json"), encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := client.TagLayout(context.Background(), ambiguous, target, ""); err == nil ||
		!strings.Contains(err.Error(), "multiple references") {
		t.Fatalf("ambiguous index err=%v", err)
	}
	if err := client.TagLayout(context.Background(), ambiguous, target, "missing:v1"); err == nil ||
		!strings.Contains(err.Error(), "is not present") {
		t.Fatalf("missing reference err=%v", err)
	}
}

func TestPushLayoutRejectsInvalidLayoutsBeforeRegistryAccess(t *testing.T) {
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
		t.Fatal("invalid layout reached registry")
		return nil
	})}}
	target := Reference{Registry: "registry.example", Repository: "team/service", Tag: "v1"}
	for name, prepare := range map[string]func(string){
		"missing index": func(string) {},
		"invalid index": func(root string) {
			if err := os.WriteFile(filepath.Join(root, "index.json"), []byte("{"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"empty index": func(root string) {
			if err := os.WriteFile(filepath.Join(root, "index.json"), []byte(`{"manifests":[]}`), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"missing manifest": func(root string) {
			index := `{"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` +
				strings.Repeat("a", 64) + `","size":1}]}`
			if err := os.WriteFile(filepath.Join(root, "index.json"), []byte(index), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			prepare(root)
			if _, err := client.PushLayout(context.Background(), root, target, ""); err == nil {
				t.Fatal("invalid layout accepted")
			}
		})
	}
}

func TestPushArtifactFailsClosedAtEachRegistryBoundary(t *testing.T) {
	target := Reference{Registry: "registry.example", Repository: "team/service", Tag: "v1"}
	validDigest := "sha256:" + strings.Repeat("a", 64)
	if _, err := (&Client{}).PushArtifact(context.Background(), target, "invalid", "image", 1, "artifact", "payload", nil); err == nil {
		t.Fatal("invalid subject digest accepted")
	}
	for _, mediaTypes := range [][2]string{{"", "payload"}, {"artifact", ""}} {
		if _, err := (&Client{}).PushArtifact(
			context.Background(), target, validDigest, "image", 1, mediaTypes[0], mediaTypes[1], nil,
		); err == nil {
			t.Fatalf("missing media types accepted: %q", mediaTypes)
		}
	}

	headFailure := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
		return response(http.StatusInternalServerError, "", strings.NewReader("head failed"))
	})}}
	if _, err := headFailure.PushArtifact(context.Background(), target, validDigest, "image", 1, "artifact", "payload", []byte("x")); err == nil ||
		!strings.Contains(err.Error(), "head failed") {
		t.Fatalf("HEAD failure err=%v", err)
	}

	uploadFailure := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		if r.Method == http.MethodHead {
			return response(http.StatusNotFound, "", nil)
		}
		return response(http.StatusInternalServerError, "", strings.NewReader("upload failed"))
	})}}
	if _, err := uploadFailure.PushArtifact(context.Background(), target, validDigest, "image", 1, "artifact", "payload", []byte("x")); err == nil ||
		!strings.Contains(err.Error(), "upload failed") {
		t.Fatalf("upload failure err=%v", err)
	}

	manifestFailure := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		if r.Method == http.MethodHead {
			return response(http.StatusOK, "", nil)
		}
		return response(http.StatusInternalServerError, "", strings.NewReader("manifest failed"))
	})}}
	if _, err := manifestFailure.PushArtifact(context.Background(), target, validDigest, "image", 1, "artifact", "payload", []byte("x")); err == nil ||
		!strings.Contains(err.Error(), "publish artifact") {
		t.Fatalf("manifest failure err=%v", err)
	}
}

func TestRegistryURLAndDigestHelpersFailClosed(t *testing.T) {
	client := &Client{}
	if client.httpClient() != http.DefaultClient {
		t.Fatal("nil client did not use the standard HTTP client")
	}
	target := Reference{Registry: "registry.example", Repository: "team/service", Tag: "v1"}
	if _, err := client.resolveLocation(target, "https://other.example/upload"); err == nil {
		t.Fatal("cross-registry upload redirect accepted")
	}
	if _, err := client.resolveLocation(target, "https://registry.example/%zz"); err == nil {
		t.Fatal("malformed upload location accepted")
	}
	resolved, err := client.resolveLocation(target, "/v2/upload")
	if err != nil || resolved.String() != "https://registry.example/v2/upload" {
		t.Fatalf("default location=%v err=%v", resolved, err)
	}
	client.Scheme = "http"
	resolved, err = client.resolveLocation(target, "next")
	if err != nil || resolved.Scheme != "http" {
		t.Fatalf("configured location=%v err=%v", resolved, err)
	}
	if _, err := digestHex("sha256:" + strings.Repeat("z", 64)); err == nil {
		t.Fatal("non-hex digest accepted")
	}
}

func TestClientTransportAndTokenFailuresAreActionable(t *testing.T) {
	endpoint, _ := url.Parse("https://registry.example/v2/team/service/blobs/uploads/")
	client := &Client{HTTP: &http.Client{Transport: roundTripErrorFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("network unavailable")
	})}}
	if _, err := client.doURL(context.Background(), http.MethodGet, endpoint, nil, "", "", 0); err == nil ||
		!strings.Contains(err.Error(), "network unavailable") {
		t.Fatalf("transport err=%v", err)
	}

	client = &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		if r.URL.Host == "registry.example" {
			result := response(http.StatusUnauthorized, "", nil)
			result.Header.Set("WWW-Authenticate", `Bearer realm="https://auth.example/token"`)
			return result
		}
		return response(http.StatusOK, "", strings.NewReader(`{"token":"issued"}`))
	})}}
	if _, err := client.doURL(context.Background(), http.MethodPatch, endpoint,
		&oneShotReader{data: []byte("payload")}, "application/octet-stream", "", 7); err == nil ||
		!strings.Contains(err.Error(), "non-replayable") {
		t.Fatal("non-replayable authenticated upload accepted")
	}

	client = &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		if r.URL.Host == "registry.example" {
			result := response(http.StatusUnauthorized, "", nil)
			result.Header.Set("WWW-Authenticate", `Bearer realm="https://auth.example/token"`)
			return result
		}
		return response(http.StatusOK, "", strings.NewReader("{"))
	})}}
	if _, err := client.doURL(context.Background(), http.MethodGet, endpoint, nil, "", "", 0); err == nil ||
		!strings.Contains(err.Error(), "decode bearer token") {
		t.Fatalf("malformed token err=%v", err)
	}
}

type oneShotReader struct{ data []byte }

func (r *oneShotReader) Read(buffer []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(buffer, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestPushLayoutUploadsBlobsBeforeDigestAndTag(t *testing.T) {
	var mu sync.Mutex
	uploads := map[string][]byte{}
	known := map[string]bool{}
	var events []string
	nextUpload := 0

	transport := roundTripFunc(func(r *http.Request) *http.Response {
		mu.Lock()
		defer mu.Unlock()
		path := r.URL.Path
		switch {
		case r.Method == http.MethodHead && strings.Contains(path, "/blobs/"):
			digest, _ := url.PathUnescape(path[strings.LastIndex(path, "/")+1:])
			if known[digest] {
				return response(http.StatusOK, "", nil)
			}
			return response(http.StatusNotFound, "", nil)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/blobs/uploads/"):
			nextUpload++
			id := fmt.Sprintf("%d", nextUpload)
			return response(http.StatusAccepted, "/upload/"+id, nil)
		case r.Method == http.MethodPatch && strings.HasPrefix(path, "/upload/"):
			data, _ := io.ReadAll(r.Body)
			uploads[path] = data
			return response(http.StatusAccepted, path, nil)
		case r.Method == http.MethodPut && strings.HasPrefix(path, "/upload/"):
			digest := r.URL.Query().Get("digest")
			if digest == "" || len(uploads[path]) == 0 {
				t.Errorf("invalid completed upload digest=%q bytes=%d", digest, len(uploads[path]))
			}
			known[digest] = true
			events = append(events, "blob")
			return response(http.StatusCreated, "", nil)
		case r.Method == http.MethodPut && strings.Contains(path, "/manifests/"):
			reference, _ := url.PathUnescape(path[strings.LastIndex(path, "/")+1:])
			_, _ = io.Copy(io.Discard, r.Body)
			events = append(events, "manifest:"+reference)
			return response(http.StatusCreated, "", nil)
		default:
			return response(http.StatusNotFound, "", strings.NewReader(r.Method+" "+path))
		}
	})

	target := Reference{Registry: "registry.example", Repository: "team/service", Tag: "v1"}
	result, err := (&Client{HTTP: &http.Client{Transport: transport}, Scheme: "https"}).PushLayout(context.Background(), testLayout(t), target, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Digest == "" || result.Reference != "registry.example/team/service@"+result.Digest || result.Blobs == 0 {
		t.Fatalf("result=%+v", result)
	}
	if len(events) < 3 || events[len(events)-2] != "manifest:"+result.Digest || events[len(events)-1] != "manifest:v1" {
		t.Fatalf("publication was not digest-before-tag: %v", events)
	}
	client := &Client{HTTP: &http.Client{Transport: transport}, Scheme: "https"}
	byDigest, err := client.PushLayoutByDigest(context.Background(), testLayout(t), target, "")
	if err != nil {
		t.Fatal(err)
	}
	if byDigest.Digest != result.Digest || events[len(events)-1] != "manifest:"+result.Digest {
		t.Fatalf("digest-only publication moved a tag: result=%+v events=%v", byDigest, events)
	}
	if err := client.TagLayout(context.Background(), testLayout(t), target, ""); err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1] != "manifest:v1" {
		t.Fatalf("explicit tag update missing: %v", events)
	}
	artifact, err := (&Client{HTTP: &http.Client{Transport: transport}, Scheme: "https"}).PushArtifact(
		context.Background(), target, result.Digest, result.MediaType, result.Size,
		"application/vnd.secure-oci.sbom.v1+json", "application/json", []byte(`{"components":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Digest == "" || events[len(events)-1] != "manifest:"+artifact.Digest {
		t.Fatalf("artifact=%+v events=%v", artifact, events)
	}
}

func TestPushLayoutRejectsUntrustedUploadRedirect(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) *http.Response {
		if r.Method == http.MethodHead {
			return response(http.StatusNotFound, "", nil)
		}
		return response(http.StatusAccepted, "https://attacker.example/upload", nil)
	})
	_, err := (&Client{HTTP: &http.Client{Transport: transport}}).PushLayout(context.Background(), testLayout(t),
		Reference{Registry: "registry.example", Repository: "team/service", Tag: "v1"}, "")
	if err == nil || !strings.Contains(err.Error(), "changed registry host") {
		t.Fatalf("err=%v", err)
	}
}

func TestUploadBlobResumesFromRegistryRange(t *testing.T) {
	var stored []byte
	patches := 0
	transport := roundTripFunc(func(r *http.Request) *http.Response {
		switch r.Method {
		case http.MethodPost:
			return response(http.StatusAccepted, "/upload/resume", nil)
		case http.MethodPatch:
			payload, _ := io.ReadAll(r.Body)
			patches++
			if patches == 1 {
				stored = append(stored, payload[:3]...)
				return response(http.StatusInternalServerError, "", nil)
			}
			stored = append(stored, payload...)
			return response(http.StatusAccepted, "/upload/resume", nil)
		case http.MethodGet:
			result := response(http.StatusNoContent, "", nil)
			result.Header.Set("Range", "0-2")
			return result
		case http.MethodPut:
			return response(http.StatusCreated, "", nil)
		default:
			return response(http.StatusNotFound, "", nil)
		}
	})
	client := &Client{HTTP: &http.Client{Transport: transport}}
	target := Reference{Registry: "registry.example", Repository: "team/service", Tag: "v1"}
	payload := []byte("resumable-payload")
	if err := client.uploadBlob(context.Background(), target, "sha256:"+strings.Repeat("a", 64),
		int64(len(payload)), bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(payload) || patches != 2 {
		t.Fatalf("stored=%q patches=%d", stored, patches)
	}
}

func TestUploadBlobResumesAcrossClientProcesses(t *testing.T) {
	var stored []byte
	starts := 0
	failFirstProcess := true
	transport := roundTripFunc(func(r *http.Request) *http.Response {
		switch r.Method {
		case http.MethodPost:
			starts++
			return response(http.StatusAccepted, "/upload/persisted", nil)
		case http.MethodPatch:
			payload, _ := io.ReadAll(r.Body)
			if failFirstProcess {
				stored = append(stored, payload[:3]...)
				return response(http.StatusInternalServerError, "", nil)
			}
			stored = append(stored, payload...)
			return response(http.StatusAccepted, "/upload/persisted", nil)
		case http.MethodGet:
			if failFirstProcess {
				return response(http.StatusInternalServerError, "", nil)
			}
			result := response(http.StatusNoContent, "", nil)
			result.Header.Set("Range", "0-2")
			return result
		case http.MethodPut:
			return response(http.StatusCreated, "", nil)
		default:
			return response(http.StatusNotFound, "", nil)
		}
	})
	target := Reference{Registry: "registry.example", Repository: "team/service", Tag: "v1"}
	payload := []byte("survives-process-crash")
	digest := "sha256:" + strings.Repeat("c", 64)
	sessionDir := t.TempDir()
	first := &Client{HTTP: &http.Client{Transport: transport}, SessionDir: sessionDir}
	if err := first.uploadBlob(context.Background(), target, digest, int64(len(payload)),
		bytes.NewReader(payload)); err == nil {
		t.Fatal("first process unexpectedly completed")
	}
	failFirstProcess = false
	second := &Client{HTTP: &http.Client{Transport: transport}, SessionDir: sessionDir}
	if err := second.uploadBlob(context.Background(), target, digest, int64(len(payload)),
		bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || !bytes.Equal(stored, payload) {
		t.Fatalf("starts=%d stored=%q want=%q", starts, stored, payload)
	}
	entries, err := os.ReadDir(sessionDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("completed session was not removed: entries=%d err=%v", len(entries), err)
	}
}

func TestUploadBlobUsesCrossRepositoryMount(t *testing.T) {
	var requests int
	transport := roundTripFunc(func(r *http.Request) *http.Response {
		requests++
		if r.Method != http.MethodPost ||
			r.URL.Query().Get("mount") == "" ||
			r.URL.Query().Get("from") != "base/images" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		return response(http.StatusCreated, "", nil)
	})
	client := &Client{
		HTTP: &http.Client{Transport: transport}, MountFrom: "base/images",
	}
	target := Reference{Registry: "registry.example", Repository: "team/service", Tag: "v1"}
	if err := client.uploadBlob(context.Background(), target, "sha256:"+strings.Repeat("b", 64),
		7, bytes.NewReader([]byte("payload"))); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests=%d", requests)
	}
}

// TestUploadBlobFallsBackToNormalUploadWhenMountIsNotHonored covers a
// registry that accepts the `mount`/`from` query parameters without error
// but doesn't actually cross-mount the blob (OCI Distribution spec:
// responding 202 with a Location instead of 201) - the same "start a
// normal upload session" response an initial POST without mount params
// would get, so uploadBlob's existing generic 202 handling must carry the
// chunk upload through rather than treating it as a mount failure.
func TestUploadBlobFallsBackToNormalUploadWhenMountIsNotHonored(t *testing.T) {
	var patched []byte
	transport := roundTripFunc(func(r *http.Request) *http.Response {
		switch r.Method {
		case http.MethodPost:
			if r.URL.Query().Get("mount") == "" || r.URL.Query().Get("from") != "base/images" {
				t.Errorf("expected a mount request, got %s", r.URL)
			}
			return response(http.StatusAccepted, "/upload/not-mounted", nil)
		case http.MethodPatch:
			payload, _ := io.ReadAll(r.Body)
			patched = append(patched, payload...)
			return response(http.StatusAccepted, "/upload/not-mounted", nil)
		case http.MethodPut:
			return response(http.StatusCreated, "", nil)
		default:
			return response(http.StatusNotFound, "", nil)
		}
	})
	client := &Client{HTTP: &http.Client{Transport: transport}, MountFrom: "base/images"}
	target := Reference{Registry: "registry.example", Repository: "team/service", Tag: "v1"}
	payload := []byte("not-actually-mounted")
	if err := client.uploadBlob(context.Background(), target, "sha256:"+strings.Repeat("c", 64),
		int64(len(payload)), bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	if string(patched) != string(payload) {
		t.Fatalf("patched=%q, want the full payload uploaded through the fallback session", patched)
	}
}

// TestUploadBlobRejectsInconsistentRangeDuringReconciliation covers a
// registry whose reconciliation GET (used after a failed PATCH) reports an
// upload offset outside the chunk that was just attempted - a
// non-compliant or corrupted response uploadBlob must reject outright
// rather than silently resuming from a bogus position, which could either
// skip real bytes or re-send bytes the registry never asked for.
func TestUploadBlobRejectsInconsistentRangeDuringReconciliation(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) *http.Response {
		switch r.Method {
		case http.MethodPost:
			return response(http.StatusAccepted, "/upload/inconsistent", nil)
		case http.MethodPatch:
			_, _ = io.ReadAll(r.Body)
			return response(http.StatusInternalServerError, "", nil)
		case http.MethodGet:
			result := response(http.StatusNoContent, "", nil)
			result.Header.Set("Range", "0-999999")
			return result
		default:
			return response(http.StatusNotFound, "", nil)
		}
	})
	client := &Client{HTTP: &http.Client{Transport: transport}}
	target := Reference{Registry: "registry.example", Repository: "team/service", Tag: "v1"}
	payload := []byte("short-payload")
	err := client.uploadBlob(context.Background(), target, "sha256:"+strings.Repeat("e", 64),
		int64(len(payload)), bytes.NewReader(payload))
	if err == nil || !strings.Contains(err.Error(), "outside current chunk") {
		t.Fatalf("err=%v, want a rejected-inconsistent-range error", err)
	}
}

func TestClientCompletesBearerAuthenticationChallenge(t *testing.T) {
	var registryRequests, tokenRequests int
	transport := roundTripFunc(func(r *http.Request) *http.Response {
		switch r.URL.Host {
		case "auth.example":
			tokenRequests++
			username, password, ok := r.BasicAuth()
			if !ok || username != "robot" || password != "secret" ||
				r.URL.Query().Get("service") != "registry.example" ||
				r.URL.Query().Get("scope") != "repository:team/service:pull,push" {
				t.Errorf("invalid token request: %s auth=%q/%q", r.URL, username, password)
			}
			result := response(http.StatusOK, "", strings.NewReader(`{"access_token":"issued-token"}`))
			result.Header.Set("Content-Type", "application/json")
			return result
		case "registry.example":
			registryRequests++
			if r.Header.Get("Authorization") == "Bearer issued-token" {
				return response(http.StatusOK, "", nil)
			}
			result := response(http.StatusUnauthorized, "", nil)
			result.Header.Set("WWW-Authenticate",
				`Bearer realm="https://auth.example/token",service="registry.example",scope="repository:team/service:pull,push"`)
			return result
		default:
			t.Errorf("unexpected host %q", r.URL.Host)
			return response(http.StatusInternalServerError, "", nil)
		}
	})
	client := &Client{
		HTTP: &http.Client{Transport: transport}, Username: "robot", Password: "secret",
	}
	exists, err := client.blobExists(context.Background(),
		Reference{Registry: "registry.example", Repository: "team/service", Tag: "v1"},
		"sha256:"+strings.Repeat("d", 64))
	if err != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
	if registryRequests != 2 || tokenRequests != 1 || client.currentToken() != "issued-token" {
		t.Fatalf("registry=%d token=%d cached=%q", registryRequests, tokenRequests, client.currentToken())
	}
}

func TestBearerAuthenticationFailsClosed(t *testing.T) {
	client := &Client{}
	for _, challenge := range []string{
		"", "Basic realm=\"example\"", "Bearer realm=\"not a URL\"",
		`Bearer realm="http://auth.example/token"`,
	} {
		if err := client.authorize(context.Background(), challenge); err == nil {
			t.Fatalf("challenge %q accepted", challenge)
		}
	}
	transport := roundTripFunc(func(r *http.Request) *http.Response {
		return response(http.StatusOK, "", strings.NewReader(`{"token":""}`))
	})
	client.HTTP = &http.Client{Transport: transport}
	if err := client.authorize(context.Background(),
		`Bearer realm="https://auth.example/token"`); err == nil {
		t.Fatal("empty token accepted")
	}
}

func TestManifestSelectionIsExplicitForCatalogs(t *testing.T) {
	if _, err := selectManifest(nil, ""); err == nil {
		t.Fatal("empty index accepted")
	}
	manifests := []descriptor{
		{Digest: "sha256:" + strings.Repeat("a", 64),
			Annotations: map[string]string{"org.opencontainers.image.ref.name": "example/a:v1"}},
		{Digest: "sha256:" + strings.Repeat("b", 64),
			Annotations: map[string]string{"org.opencontainers.image.ref.name": "example/b:v1"}},
	}
	if _, err := selectManifest(manifests, ""); err == nil {
		t.Fatal("ambiguous catalog accepted")
	}
	if _, err := selectManifest(manifests, "missing:v1"); err == nil {
		t.Fatal("missing source accepted")
	}
	selected, err := selectManifest(manifests, "example/b:v1")
	if err != nil || selected.Digest != manifests[1].Digest {
		t.Fatalf("selected=%+v err=%v", selected, err)
	}
}

func TestAuthParameterParserRejectsAmbiguity(t *testing.T) {
	for _, value := range []string{
		"novalue", `realm="unterminated`, `realm=""`,
		`realm="https://one",realm="https://two"`,
		`realm="https://one" trailing`,
	} {
		if _, err := parseAuthParameters(value); err == nil {
			t.Fatalf("parameters %q accepted", value)
		}
	}
	values, err := parseAuthParameters(`realm=https://auth.example/token,service=registry`)
	if err != nil || values["service"] != "registry" {
		t.Fatalf("values=%v err=%v", values, err)
	}
}

type roundTripFunc func(*http.Request) *http.Response

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request), nil
}

type roundTripErrorFunc func(*http.Request) (*http.Response, error)

func (f roundTripErrorFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func response(status int, location string, body io.Reader) *http.Response {
	if body == nil {
		body = strings.NewReader("")
	}
	header := make(http.Header)
	if location != "" {
		header.Set("Location", location)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(body)}
}

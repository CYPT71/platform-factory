package cache

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlobHTTPRoundTripVerifiesContent(t *testing.T) {
	source, _ := Open(t.TempDir())
	destination, _ := Open(t.TempDir())
	descriptor, _ := source.Put(strings.NewReader("distributed blob"))
	server := httptest.NewServer(BlobHandler(source, 1024))
	defer server.Close()
	if err := PullBlob(context.Background(), server.Client(), server.URL, destination, descriptor, 1024); err != nil {
		t.Fatal(err)
	}
	if err := destination.Verify(descriptor.Digest); err != nil {
		t.Fatal(err)
	}
}

func TestBlobHTTPRejectsInvalidDigestsQueriesAndMethods(t *testing.T) {
	store, _ := Open(t.TempDir())
	server := httptest.NewServer(BlobHandler(store, 1024))
	defer server.Close()
	digest := "sha256:" + strings.Repeat("a", 64)

	if response, err := server.Client().Get(server.URL + "/not-a-digest"); err != nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid digest: status=%v err=%v", response, err)
	}
	if response, err := server.Client().Get(server.URL + "/" + digest + "?x=1"); err != nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("query string: status=%v err=%v", response, err)
	}
	if response, err := server.Client().Get(server.URL + "/" + digest); err != nil || response.StatusCode != http.StatusNotFound {
		t.Fatalf("missing blob GET: status=%v err=%v", response, err)
	}
	request, _ := http.NewRequest(http.MethodDelete, server.URL+"/"+digest, nil)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("method not allowed: status=%d", response.StatusCode)
	}
}

func TestPullBlobRejectsInvalidArgumentsAndResponses(t *testing.T) {
	destination, _ := Open(t.TempDir())
	valid := Descriptor{Digest: "sha256:" + strings.Repeat("a", 64), Size: 4}

	if err := PullBlob(context.Background(), nil, "http://example", destination, valid, 1024); err == nil {
		t.Fatal("expected an error for a nil client")
	}
	if err := PullBlob(context.Background(), http.DefaultClient, "http://example", nil, valid, 1024); err == nil {
		t.Fatal("expected an error for a nil destination")
	}
	if err := PullBlob(context.Background(), http.DefaultClient, "http://example", destination, Descriptor{Digest: valid.Digest, Size: 2048}, 1024); err == nil {
		t.Fatal("expected an error when the descriptor size exceeds the limit")
	}
	if err := PullBlob(context.Background(), http.DefaultClient, "http://example", destination, Descriptor{Digest: "not-a-digest", Size: 4}, 1024); err == nil {
		t.Fatal("expected an error for an invalid digest")
	}

	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer notFound.Close()
	if err := PullBlob(context.Background(), notFound.Client(), notFound.URL, destination, valid, 1024); err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
}

func TestBlobHTTPRejectsMismatchAndUnboundedRequests(t *testing.T) {
	store, _ := Open(t.TempDir())
	server := httptest.NewServer(BlobHandler(store, 16))
	defer server.Close()
	digest := "sha256:" + strings.Repeat("a", 64)
	request, _ := http.NewRequest(http.MethodPut, server.URL+"/"+digest, bytes.NewReader([]byte(strings.Repeat("x", 17))))
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodPut, server.URL+"/"+digest, bytes.NewReader([]byte("wrong")))
	response, err = server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("mismatch status=%d", response.StatusCode)
	}
	if entries, err := os.ReadDir(filepath.Join(filepath.Dir(store.records), "blobs", "sha256")); err != nil || len(entries) != 0 {
		t.Fatalf("mismatched upload mutated CAS: entries=%v err=%v", entries, err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "tampered") }))
	defer bad.Close()
	destination, _ := Open(t.TempDir())
	descriptor := Descriptor{Digest: digest, Size: 8}
	if err := PullBlob(context.Background(), bad.Client(), bad.URL, destination, descriptor, 1024); err == nil {
		t.Fatal("accepted corrupt remote blob")
	}
}

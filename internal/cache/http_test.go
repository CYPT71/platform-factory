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

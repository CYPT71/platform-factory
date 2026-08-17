package marketplace

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchCatalogHappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"abc123"`)
		w.Write([]byte(validCatalogJSON("https://203.0.113.10/a.git")))
	}))
	defer server.Close()

	catalog, etag, skipped, err := FetchCatalog(context.Background(), server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 {
		t.Fatalf("unexpected skips: %+v", skipped)
	}
	if len(catalog.Repositories) != 1 || catalog.Repositories[0] != "https://203.0.113.10/a.git" {
		t.Fatalf("unexpected catalog: %+v", catalog)
	}
	if etag != `"abc123"` {
		t.Fatalf("etag = %q, want captured", etag)
	}
}

func TestFetchCatalogRejectsHTTPErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom"))
	}))
	defer server.Close()

	_, _, _, err := FetchCatalog(context.Background(), server.URL, server.Client())
	if err == nil || !errors.Is(err, ErrCatalogFetch) {
		t.Fatalf("want ErrCatalogFetch, got %v", err)
	}
}

func TestFetchCatalogRejectsNotFoundDistinctly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, _, _, err := FetchCatalog(context.Background(), server.URL, server.Client())
	if err == nil || !errors.Is(err, ErrCatalogNotFound) {
		t.Fatalf("want ErrCatalogNotFound, got %v", err)
	}
}

func TestFetchCatalogRejectsMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{not json`))
	}))
	defer server.Close()

	_, _, _, err := FetchCatalog(context.Background(), server.URL, server.Client())
	if err == nil {
		t.Fatal("want malformed JSON rejected")
	}
}

func TestFetchCatalogRejectsEmptyURL(t *testing.T) {
	if _, _, _, err := FetchCatalog(context.Background(), "", nil); err == nil {
		t.Fatal("want an empty catalog URL rejected")
	}
}

func TestFetchCatalogTimesOutOnASlowServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte(validCatalogJSON()))
	}))
	defer server.Close()

	client := server.Client()
	client.Timeout = 20 * time.Millisecond
	_, _, _, err := FetchCatalog(context.Background(), server.URL, client)
	if err == nil {
		t.Fatal("want a slow server to time out")
	}
}

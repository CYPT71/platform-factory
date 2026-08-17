package marketplace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

func TestDetectPluginRepository(t *testing.T) {
	dir := newTestRepo(t)
	runFixtureGit(t, dir, "remote", "add", "origin", "https://203.0.113.10/acme/plugin.git")

	got, err := DetectPluginRepository(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://203.0.113.10/acme/plugin.git" {
		t.Fatalf("got %q", got)
	}
}

func TestDetectPluginRepositoryRequiresOrigin(t *testing.T) {
	dir := newTestRepo(t)
	if _, err := DetectPluginRepository(context.Background(), dir); err == nil {
		t.Fatal("want an error when no origin remote is configured")
	}
}

func TestDetectPluginRepositoryRejectsUnsafeOrigin(t *testing.T) {
	dir := newTestRepo(t)
	runFixtureGit(t, dir, "remote", "add", "origin", "https://127.0.0.1/acme/plugin.git")
	if _, err := DetectPluginRepository(context.Background(), dir); err == nil {
		t.Fatal("want an origin pointing at a blocked destination refused")
	}
}

func TestValidatePluginForPublish(t *testing.T) {
	dir := newTestRepo(t)
	tagRelease(t, dir, "v1.0.0", manifestFor("acme", "v1.0.0"), "plugin.py", "print('hi')")

	manifest, err := ValidatePluginForPublish(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "acme" || manifest.Version != "v1.0.0" {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
}

func TestValidatePluginForPublishRequiresATagAtHEAD(t *testing.T) {
	dir := newTestRepo(t)
	tagRelease(t, dir, "v1.0.0", manifestFor("acme", "v1.0.0"), "plugin.py", "print('hi')")
	// A commit after the tag, so HEAD itself carries no tag.
	runFixtureGit(t, dir, "commit", "--allow-empty", "-m", "untagged follow-up")

	if _, err := ValidatePluginForPublish(context.Background(), dir); err == nil {
		t.Fatal("want an untagged HEAD refused")
	}
}

func TestValidatePluginForPublishRequiresTagToMatchManifestVersion(t *testing.T) {
	dir := newTestRepo(t)
	// plugin.yaml claims v1.0.0, but the tag actually applied is v2.0.0.
	tagRelease(t, dir, "v2.0.0", manifestFor("acme", "v1.0.0"), "plugin.py", "print('hi')")

	if _, err := ValidatePluginForPublish(context.Background(), dir); err == nil {
		t.Fatal("want a tag/manifest version mismatch refused")
	}
}

// catalogServer is a minimal in-memory catalog endpoint for exercising
// PublishRepository's GET-modify-PUT cycle, including ETag/If-Match
// concurrency, without any real network access.
type catalogServer struct {
	mu       sync.Mutex
	body     []byte // nil means "not created yet" (404 on GET)
	etag     int
	putCalls int
}

func newCatalogServer(t *testing.T, initial *Catalog) *httptest.Server {
	t.Helper()
	state := &catalogServer{}
	if initial != nil {
		data, err := json.Marshal(*initial)
		if err != nil {
			t.Fatal(err)
		}
		state.body = data
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			if state.body == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("ETag", etagValue(state.etag))
			w.Write(state.body)
		case http.MethodPut:
			state.putCalls++
			if match := r.Header.Get("If-Match"); match != "" && state.body != nil && match != etagValue(state.etag) {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			data, err := io.ReadAll(io.LimitReader(r.Body, maxCatalogBytes+1))
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			state.body = data
			state.etag++
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func etagValue(version int) string {
	return `"v` + strconv.Itoa(version) + `"`
}

func TestPublishRepositoryAddsANewRepository(t *testing.T) {
	server := newCatalogServer(t, &Catalog{Schema: CatalogSchema, Repositories: []string{"https://203.0.113.10/existing.git"}})

	added, err := PublishRepository(context.Background(), server.URL, server.Client(), "https://203.0.113.20/new.git")
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("want added=true for a genuinely new repository")
	}

	catalog, _, _, err := FetchCatalog(context.Background(), server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://203.0.113.10/existing.git", "https://203.0.113.20/new.git"}
	if len(catalog.Repositories) != 2 || catalog.Repositories[0] != want[0] || catalog.Repositories[1] != want[1] {
		t.Fatalf("got %+v, want %+v (sorted, deterministic)", catalog.Repositories, want)
	}
}

func TestPublishRepositoryIsIdempotentWhenAlreadyPresent(t *testing.T) {
	server := newCatalogServer(t, &Catalog{Schema: CatalogSchema, Repositories: []string{"https://203.0.113.10/existing.git"}})

	added, err := PublishRepository(context.Background(), server.URL, server.Client(), "https://203.0.113.10/existing.git")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Fatal("want added=false for an already-listed repository (no-op)")
	}
}

func TestPublishRepositoryBootstrapsAMissingCatalog(t *testing.T) {
	server := newCatalogServer(t, nil) // 404 until the first successful PUT

	added, err := PublishRepository(context.Background(), server.URL, server.Client(), "https://203.0.113.10/first.git")
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("want the first publish against a missing catalog to succeed")
	}
}

func TestPublishRepositoryPropagatesHTTPErrorDuringGET(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if _, err := PublishRepository(context.Background(), server.URL, server.Client(), "https://203.0.113.10/x.git"); err == nil {
		t.Fatal("want a GET failure propagated")
	}
}

func TestPublishRepositoryPropagatesHTTPErrorDuringPUT(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Write([]byte(validCatalogJSON()))
		case http.MethodPut:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	if _, err := PublishRepository(context.Background(), server.URL, server.Client(), "https://203.0.113.10/x.git"); err == nil {
		t.Fatal("want a PUT failure propagated")
	}
}

func TestPublishRepositoryReportsETagConflictDistinctly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("ETag", `"stale"`)
			w.Write([]byte(validCatalogJSON()))
		case http.MethodPut:
			// Simulates another publish having landed between this
			// GET and PUT: the server always rejects with a stale ETag.
			w.WriteHeader(http.StatusPreconditionFailed)
		}
	}))
	defer server.Close()

	_, err := PublishRepository(context.Background(), server.URL, server.Client(), "https://203.0.113.10/x.git")
	if !errors.Is(err, ErrCatalogConflict) {
		t.Fatalf("want ErrCatalogConflict, got %v", err)
	}
}

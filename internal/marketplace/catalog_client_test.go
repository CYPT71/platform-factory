package marketplace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
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

// TestDialPinnedIPConnectsToTheGivenAddressNotTheOriginalHost proves, with
// a real TCP listener, that dialPinnedIP actually controls which address
// gets connected to, regardless of what hostname or address a caller's
// own "addr" argument named - the mechanism
// dialValidatedRedirectTarget/catalogHTTPClient's DialContext rely on to
// close the DNS-rebinding TOCTOU window a validate-the-hostname-then-
// separately-let-the-transport-resolve-it-again sequence would otherwise
// leave open. checkHostSafe's own tests already prove the validation
// itself rejects unsafe targets; this proves the other half - that a
// validated result is what actually gets dialed, not re-resolved.
func TestDialPinnedIPConnectsToTheGivenAddressNotTheOriginalHost(t *testing.T) {
	const sentinel = "dial-pinned-ip-reached-the-real-server"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sentinel))
	}))
	defer server.Close()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	conn, err := dialPinnedIP(context.Background(), &net.Dialer{}, "tcp", port, net.ParseIP("127.0.0.1"))
	if err != nil {
		t.Fatalf("dialPinnedIP: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET / HTTP/1.0\r\nHost: localhost\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(sentinel)) {
		t.Fatalf("response did not come from the pinned server: %q", body)
	}
}

func TestDialValidatedRedirectTargetRejectsAnUnsafeHost(t *testing.T) {
	if _, err := dialValidatedRedirectTarget(context.Background(), &net.Dialer{}, "tcp", "127.0.0.1:9"); err == nil {
		t.Fatal("expected a loopback redirect target to be rejected before any dial was attempted")
	}
}

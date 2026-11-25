package marketplace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// catalogHTTPClient builds the client both FetchCatalog and PublishRepository
// use when the caller does not inject their own: a bounded overall timeout,
// checkRedirectSafe gating every redirect hop against the same
// destination policy repository URLs get (see catalog_security.go's
// package note for why the initial request itself is not gated the same
// way), and a DialContext that re-validates and pins every dial past the
// first one.
//
// checkRedirectSafe alone is not enough: CheckRedirect runs, then the
// Transport separately resolves the redirect target's hostname again
// when it actually connects - a second, independent lookup that a
// malicious or rebinding-capable DNS server for that hostname can answer
// differently from the one CheckRedirect's own validating lookup saw.
// Folding the check into DialContext itself closes that gap, since
// there only one lookup ever happens and its result is exactly what
// gets dialed (see checkHostSafe's own doc comment).
//
// The first dial (to the operator-configured catalog endpoint itself,
// per catalog_security.go's package note) is deliberately exempt, the
// same exemption checkRedirectSafe already does not need to make
// because CheckRedirect is never invoked for the initial request. A
// fresh *http.Client/closure is built per call (see FetchCatalog/
// PublishRepository), so this exemption can never leak across an
// unrelated request.
func catalogHTTPClient() *http.Client {
	dialer := &net.Dialer{}
	var mu sync.Mutex
	first := true
	// Clone the default transport (proxy support, idle-connection and
	// handshake timeouts, ...) rather than building a bare one from
	// scratch - only DialContext's behavior is meant to change here.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		skip := first
		first = false
		mu.Unlock()
		if skip {
			return dialer.DialContext(ctx, network, addr)
		}
		return dialValidatedRedirectTarget(ctx, dialer, network, addr)
	}
	return &http.Client{
		Timeout:       catalogFetchTimeout,
		CheckRedirect: checkRedirectSafe,
		Transport:     transport,
	}
}

// dialValidatedRedirectTarget validates addr's host via checkHostSafe and
// dials the exact IP that validation returned - never addr's hostname a
// second time, which is what would reopen the TOCTOU window
// catalogHTTPClient's doc comment describes.
func dialValidatedRedirectTarget(ctx context.Context, dialer *net.Dialer, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ip, err := checkHostSafe(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("marketplace: redirect target refused: %w", err)
	}
	return dialPinnedIP(ctx, dialer, network, port, ip)
}

// dialPinnedIP connects to ip:port directly, regardless of whatever
// hostname or address string named the connection - the mechanism
// dialValidatedRedirectTarget relies on to guarantee the address it just
// validated is exactly the one that gets connected to, with no second,
// independent resolution in between.
func dialPinnedIP(ctx context.Context, dialer *net.Dialer, network, port string, ip net.IP) (net.Conn, error) {
	return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
}

// ErrCatalogFetch wraps a non-2xx response from the catalog endpoint.
var ErrCatalogFetch = errors.New("marketplace: catalog fetch failed")

// ErrCatalogNotFound distinguishes a 404 from other fetch failures so
// PublishRepository can bootstrap a fresh, empty catalog document
// instead of refusing to publish just because nothing exists there yet.
var ErrCatalogNotFound = errors.New("marketplace: catalog not found")

// FetchCatalog downloads and strictly parses the catalog document at
// catalogURL. client may be nil, in which case catalogHTTPClient() is
// used; tests inject their own (an httptest.Server's client, typically)
// so nothing here ever touches the network. etag is the response's ETag
// header, if any - PublishRepository uses it for optimistic concurrency.
func FetchCatalog(ctx context.Context, catalogURL string, client *http.Client) (catalog Catalog, etag string, skipped []catalogSkip, err error) {
	if strings.TrimSpace(catalogURL) == "" {
		return Catalog{}, "", nil, errors.New("marketplace: catalog URL is required")
	}
	if client == nil {
		client = catalogHTTPClient()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, catalogURL, nil)
	if err != nil {
		return Catalog{}, "", nil, fmt.Errorf("marketplace: build catalog request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return Catalog{}, "", nil, fmt.Errorf("marketplace: fetch catalog: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return Catalog{}, "", nil, fmt.Errorf("%w: %s", ErrCatalogNotFound, catalogURL)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxCatalogBytes))
		return Catalog{}, "", nil, fmt.Errorf("%w: HTTP %d: %s", ErrCatalogFetch, response.StatusCode, strings.TrimSpace(string(body)))
	}
	catalog, skipped, err = DecodeCatalog(ctx, response.Body)
	if err != nil {
		return Catalog{}, "", nil, fmt.Errorf("marketplace: decode catalog: %w", err)
	}
	return catalog, response.Header.Get("ETag"), skipped, nil
}

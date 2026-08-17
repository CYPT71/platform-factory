package marketplace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// catalogHTTPClient builds the client both FetchCatalog and PublishRepository
// use when the caller does not inject their own: a bounded overall timeout,
// and checkRedirectSafe gating every redirect hop against the same
// destination policy repository URLs get (see catalog_security.go's
// package note for why the initial request itself is not gated the same
// way).
func catalogHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       catalogFetchTimeout,
		CheckRedirect: checkRedirectSafe,
	}
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

package marketplace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrCatalogConflict indicates PublishRepository's PUT was rejected
// because the catalog changed between GET and PUT (an ETag mismatch,
// surfaced by the server as HTTP 412 Precondition Failed). A concurrent
// `pf marketplace publish` is the expected cause; the caller should
// retry. When the server does not support ETag/If-Match at all, this
// lost-update race is not detected - see PublishRepository's doc
// comment for why that is an accepted, documented v1 limitation rather
// than something worked around here.
var ErrCatalogConflict = errors.New("marketplace: catalog was modified concurrently; retry")

// DetectPluginRepository resolves dir's "origin" Git remote and
// canonicalizes it through the identical validation a subscriber's
// catalog entry goes through (ValidateCatalogRepositoryURL) - publish
// refuses to register a repository that sync would itself refuse to
// treat as a safe destination.
func DetectPluginRepository(ctx context.Context, dir string) (string, error) {
	output, err := runGit(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("marketplace: detect origin remote: %w", err)
	}
	origin := strings.TrimSpace(output)
	if origin == "" {
		return "", errors.New("marketplace: repository has no origin remote configured")
	}
	canonical, err := ValidateCatalogRepositoryURL(ctx, origin)
	if err != nil {
		return "", fmt.Errorf("marketplace: origin remote %q is not publishable: %w", origin, err)
	}
	return canonical, nil
}

// ValidatePluginForPublish loads and validates dir/plugin.yaml, then
// requires HEAD to carry a tag matching the manifest's version - the
// same tag/version consistency fetchRelease already enforces at sync
// time (sync.go), caught here locally before the network is ever
// touched.
func ValidatePluginForPublish(ctx context.Context, dir string) (Manifest, error) {
	file, err := os.Open(filepath.Join(dir, ManifestFileName))
	if err != nil {
		return Manifest{}, fmt.Errorf("marketplace: open %s: %w", ManifestFileName, err)
	}
	manifest, err := DecodeManifest(file)
	closeErr := file.Close()
	if err != nil {
		return Manifest{}, err
	}
	if closeErr != nil {
		return Manifest{}, closeErr
	}
	output, err := runGit(ctx, dir, "tag", "--points-at", "HEAD")
	if err != nil {
		return Manifest{}, fmt.Errorf("marketplace: list tags at HEAD: %w", err)
	}
	for _, tag := range strings.Fields(output) {
		if normalizeVersion(tag) == normalizeVersion(manifest.Version) {
			return manifest, nil
		}
	}
	return Manifest{}, fmt.Errorf(
		"marketplace: HEAD is not tagged %s (plugin.yaml's version); run: git tag %s && git push origin %s",
		manifest.Version, manifest.Version, manifest.Version)
}

// PublishRepository registers repository (already canonicalized, e.g. by
// DetectPluginRepository) in the catalog at catalogURL if it is not
// already listed. Idempotent: added is false with a nil error when
// repository was already present, not an error condition. A missing
// catalog document (HTTP 404 on the initial GET) is treated as an empty
// one to bootstrap, rather than refused.
//
// This first, experimental version requires no authentication to
// publish - the catalog is only ever untrusted discovery (see catalog.go),
// so a hostile publish can at most add noise to what gets synced, never
// bypass any verification. It remains vulnerable to spam and to a
// competing publish silently winning a lost-update race when the server
// does not support ETag/If-Match; PublishRepository already threads an
// ETag through when the server provides one, so adding a transactional
// endpoint later needs no signature change here.
func PublishRepository(ctx context.Context, catalogURL string, client *http.Client, repository string) (added bool, err error) {
	if client == nil {
		client = catalogHTTPClient()
	}
	catalog, etag, _, err := FetchCatalog(ctx, catalogURL, client)
	if err != nil {
		if !errors.Is(err, ErrCatalogNotFound) {
			return false, err
		}
		catalog = Catalog{Schema: CatalogSchema}
		etag = ""
	}
	for _, existing := range catalog.Repositories {
		if existing == repository {
			return false, nil
		}
	}
	updated := Catalog{
		Schema:       CatalogSchema,
		Repositories: append(append([]string(nil), catalog.Repositories...), repository),
	}
	sort.Strings(updated.Repositories)
	payload, err := json.Marshal(updated)
	if err != nil {
		return false, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, catalogURL, bytes.NewReader(payload))
	if err != nil {
		return false, fmt.Errorf("marketplace: build catalog publish request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if etag != "" {
		request.Header.Set("If-Match", etag)
	}
	response, err := client.Do(request)
	if err != nil {
		return false, fmt.Errorf("marketplace: publish catalog: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusPreconditionFailed {
		return false, ErrCatalogConflict
	}
	switch response.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return true, nil
	default:
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxCatalogBytes))
		return false, fmt.Errorf("marketplace: publish catalog: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
}

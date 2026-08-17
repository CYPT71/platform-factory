package marketplace

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/CYPT71/platform-factory/internal/strictjson"
)

// CatalogSchema identifies the catalog document format, exact-match
// versioned the same way Manifest.APIVersion is (a future v2 is a
// different string, not a range check).
const CatalogSchema = "platform-factory.dev/catalog/v1"

const (
	maxCatalogBytes        = 1 << 20 // 1 MiB: a discovery document, not a payload store
	maxCatalogRepositories = 500
	maxCatalogRedirects    = 3
	catalogFetchTimeout    = 10 * time.Second
)

// DefaultPerRepositorySyncTimeout bounds a single repository's own sync
// (git ls-remote plus a shallow clone per tag) when syncing a batch via
// SyncAllWithOptions, so one repository with a slow or hanging Git host
// - a real risk once repositories can be discovered from an untrusted
// catalog instead of only hand-added - cannot stall every repository
// after it in the same sync.
const DefaultPerRepositorySyncTimeout = 60 * time.Second

// Catalog is UNTRUSTED DISCOVERY, never a trust decision: it names
// repositories a client MAY try to sync, nothing more. It deliberately
// carries no "verified"/"official"/"trusted" field, no checksum, no
// signature, no permissions - none of that would mean anything here,
// since anyone able to write to a public catalog endpoint (see
// publish.go; this first version has no catalog authentication at all)
// could set it. Every repository listed still goes through the exact
// same manifest/SemVer/checksum/signature verification any
// marketplace-sources.json entry already goes through in sync.go - nothing
// about install-time trust changes because a repository arrived via a
// catalog instead of by hand.
type Catalog struct {
	Schema       string   `json:"schema"`
	Repositories []string `json:"repositories"`
}

type catalogSkip struct {
	Repository string
	Err        error
}

var errCatalogTooLarge = fmt.Errorf("marketplace: catalog exceeds %d bytes", maxCatalogBytes)

func errCatalogSchema(got string) error {
	return fmt.Errorf("marketplace: unsupported catalog schema %q (want %q)", got, CatalogSchema)
}

func errTooManyRepositories(count int) error {
	return fmt.Errorf("marketplace: catalog lists %d repositories, exceeding the %d limit", count, maxCatalogRepositories)
}

// DecodeCatalog strictly decodes and validates a catalog document from
// r, which must be pre-bounded by the caller (FetchCatalog does this for
// a network response; a caller reading a local file should do the same).
// Structural problems (wrong schema, oversized, too many entries)
// reject the whole document. An individual malformed or unsafe
// repository entry is dropped and reported in skipped, not fatal to the
// rest - the same "one bad entry doesn't sink the batch" posture
// SyncSource already takes with a bad tag.
func DecodeCatalog(ctx context.Context, r io.Reader) (catalog Catalog, skipped []catalogSkip, err error) {
	data, err := io.ReadAll(io.LimitReader(r, maxCatalogBytes+1))
	if err != nil {
		return Catalog{}, nil, err
	}
	if len(data) > maxCatalogBytes {
		return Catalog{}, nil, errCatalogTooLarge
	}
	var decoded Catalog
	if err := strictjson.Decode(data, &decoded); err != nil {
		return Catalog{}, nil, err
	}
	if decoded.Schema != CatalogSchema {
		return Catalog{}, nil, errCatalogSchema(decoded.Schema)
	}
	if len(decoded.Repositories) > maxCatalogRepositories {
		return Catalog{}, nil, errTooManyRepositories(len(decoded.Repositories))
	}

	seen := make(map[string]bool, len(decoded.Repositories))
	var clean []string
	for _, raw := range decoded.Repositories {
		normalized, err := ValidateCatalogRepositoryURL(ctx, raw)
		if err != nil {
			skipped = append(skipped, catalogSkip{Repository: raw, Err: err})
			continue
		}
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		clean = append(clean, normalized)
	}
	sort.Strings(clean)
	return Catalog{Schema: decoded.Schema, Repositories: clean}, skipped, nil
}

// defaultCatalogURL is this repository's own seed catalog
// (marketplace-catalog.json at the repository root), served as a plain
// file via GitHub's raw content endpoint - no separate hosting
// infrastructure, just whatever is committed to main. It starts empty
// (see marketplace-catalog.json) and is exactly as untrusted as any
// other catalog: publishing to it still goes through PublishRepository's
// own checks, and every repository it lists still goes through the full
// manifest/SemVer/checksum/signature verification at sync time. This is
// a provisional default "for now" - swap PLATFORM_FACTORY_MARKETPLACE_CATALOG_URL
// to point anywhere else, including an internal/enterprise catalog, at
// any time.
const defaultCatalogURL = "https://raw.githubusercontent.com/CYPT71/platform-factory/main/marketplace-catalog.json"

// DefaultCatalogURL reads the configured public catalog endpoint, falling
// back to this repository's own seed catalog (defaultCatalogURL) when
// PLATFORM_FACTORY_MARKETPLACE_CATALOG_URL is unset.
func DefaultCatalogURL() string {
	if url := os.Getenv("PLATFORM_FACTORY_MARKETPLACE_CATALOG_URL"); url != "" {
		return url
	}
	return defaultCatalogURL
}

package marketplace

import (
	"context"
	"strings"
	"testing"
)

func validCatalogJSON(repositories ...string) string {
	quoted := make([]string, len(repositories))
	for i, repo := range repositories {
		quoted[i] = `"` + repo + `"`
	}
	return `{"schema":"` + CatalogSchema + `","repositories":[` + strings.Join(quoted, ",") + `]}`
}

func TestDecodeCatalogAcceptsAValidDocument(t *testing.T) {
	catalog, skipped, err := DecodeCatalog(context.Background(), strings.NewReader(
		validCatalogJSON("https://203.0.113.10/a.git", "https://203.0.113.11/b.git")))
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 {
		t.Fatalf("unexpected skips: %+v", skipped)
	}
	if len(catalog.Repositories) != 2 {
		t.Fatalf("want 2 repositories, got %+v", catalog.Repositories)
	}
}

func TestDecodeCatalogRejectsUnknownSchema(t *testing.T) {
	_, _, err := DecodeCatalog(context.Background(), strings.NewReader(
		`{"schema":"platform-factory.dev/catalog/v99","repositories":[]}`))
	if err == nil {
		t.Fatal("want unknown schema rejected")
	}
}

func TestDecodeCatalogRejectsInvalidJSON(t *testing.T) {
	_, _, err := DecodeCatalog(context.Background(), strings.NewReader(`{not json`))
	if err == nil {
		t.Fatal("want invalid JSON rejected")
	}
}

func TestDecodeCatalogRejectsUnknownField(t *testing.T) {
	_, _, err := DecodeCatalog(context.Background(), strings.NewReader(
		`{"schema":"`+CatalogSchema+`","repositories":[],"official":true}`))
	if err == nil {
		t.Fatal("want an unknown top-level field rejected (strict decoding)")
	}
}

func TestDecodeCatalogRejectsOversizedDocument(t *testing.T) {
	oversized := strings.NewReader(strings.Repeat("a", maxCatalogBytes+2))
	_, _, err := DecodeCatalog(context.Background(), oversized)
	if err == nil {
		t.Fatal("want an oversized document rejected")
	}
}

func TestDecodeCatalogRejectsTooManyRepositories(t *testing.T) {
	repos := make([]string, maxCatalogRepositories+1)
	for i := range repos {
		repos[i] = "https://203.0.113.10/plugin.git"
	}
	_, _, err := DecodeCatalog(context.Background(), strings.NewReader(validCatalogJSON(repos...)))
	if err == nil {
		t.Fatal("want too many repositories rejected")
	}
}

func TestDecodeCatalogDeduplicatesRepositories(t *testing.T) {
	catalog, _, err := DecodeCatalog(context.Background(), strings.NewReader(
		validCatalogJSON("https://203.0.113.10/a.git", "https://203.0.113.10/a.git")))
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Repositories) != 1 {
		t.Fatalf("want deduplicated to 1 entry, got %+v", catalog.Repositories)
	}
}

func TestDecodeCatalogSortsRepositoriesDeterministically(t *testing.T) {
	catalog, _, err := DecodeCatalog(context.Background(), strings.NewReader(
		validCatalogJSON("https://203.0.113.20/z.git", "https://203.0.113.10/a.git")))
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Repositories[0] != "https://203.0.113.10/a.git" {
		t.Fatalf("want sorted order, got %+v", catalog.Repositories)
	}
}

func TestDecodeCatalogDropsOneInvalidURLWithoutFailingTheRest(t *testing.T) {
	catalog, skipped, err := DecodeCatalog(context.Background(), strings.NewReader(
		validCatalogJSON("https://203.0.113.10/good.git", "http://203.0.113.10/insecure.git")))
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Repositories) != 1 || catalog.Repositories[0] != "https://203.0.113.10/good.git" {
		t.Fatalf("want only the valid entry kept, got %+v", catalog.Repositories)
	}
	if len(skipped) != 1 {
		t.Fatalf("want the invalid entry reported as skipped, got %+v", skipped)
	}
}

func TestDecodeCatalogDropsEntryWithCredentials(t *testing.T) {
	catalog, skipped, err := DecodeCatalog(context.Background(), strings.NewReader(
		validCatalogJSON("https://user:pass@203.0.113.10/evil.git")))
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Repositories) != 0 {
		t.Fatalf("want the credentialed entry dropped, got %+v", catalog.Repositories)
	}
	if len(skipped) != 1 {
		t.Fatalf("want it reported as skipped, got %+v", skipped)
	}
}

func TestDecodeCatalogRejectsPrivateAndLoopbackEntries(t *testing.T) {
	catalog, skipped, err := DecodeCatalog(context.Background(), strings.NewReader(
		validCatalogJSON("https://127.0.0.1/a.git", "https://169.254.169.254/b.git", "https://10.0.0.1/c.git")))
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Repositories) != 0 {
		t.Fatalf("want every hostile-destination entry dropped, got %+v", catalog.Repositories)
	}
	if len(skipped) != 3 {
		t.Fatalf("want all 3 reported as skipped, got %+v", skipped)
	}
}

// TestCatalogSchemaCannotCarryTrustMetadata is the core security
// invariant: the catalog wire format is `{"schema": ..., "repositories":
// [...string]}` - repository entries are bare strings, so a hostile
// catalog cannot even attempt to attach a "verified"/"official"/
// "trusted"/"checksum" field to one. A document that tries (repository
// entries as objects instead of strings) fails to decode outright,
// rather than silently keeping whatever data it could parse.
func TestCatalogSchemaCannotCarryTrustMetadata(t *testing.T) {
	hostile := `{"schema":"` + CatalogSchema + `","repositories":[{"url":"https://203.0.113.10/a.git","verified":true,"official":true}]}`
	_, _, err := DecodeCatalog(context.Background(), strings.NewReader(hostile))
	if err == nil {
		t.Fatal("want a catalog that tries to attach trust metadata to an entry to be rejected outright")
	}
}

func TestDefaultCatalogURLFallsBackToTheRepositorysOwnSeedCatalog(t *testing.T) {
	t.Setenv("PLATFORM_FACTORY_MARKETPLACE_CATALOG_URL", "")
	if got := DefaultCatalogURL(); got != defaultCatalogURL {
		t.Fatalf("want the repository's own seed catalog as the fallback, got %q", got)
	}
	t.Setenv("PLATFORM_FACTORY_MARKETPLACE_CATALOG_URL", "https://example.test/catalog.json")
	if got := DefaultCatalogURL(); got != "https://example.test/catalog.json" {
		t.Fatalf("got %q", got)
	}
}

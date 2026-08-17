package marketplace

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateCatalogRepositoryURLRejectsNonHTTPS(t *testing.T) {
	for _, raw := range []string{
		"http://example.com/plugin.git",
		"git://example.com/plugin.git",
		"ftp://example.com/plugin.git",
		"not a url at all",
	} {
		if _, err := ValidateCatalogRepositoryURL(context.Background(), raw); err == nil {
			t.Errorf("%q: want rejected, got accepted", raw)
		}
	}
}

func TestValidateCatalogRepositoryURLRejectsCredentials(t *testing.T) {
	_, err := ValidateCatalogRepositoryURL(context.Background(), "https://user:pass@example.com/plugin.git")
	if err == nil {
		t.Fatal("want credentials-in-URL rejected")
	}
}

func TestValidateCatalogRepositoryURLRejectsLoopbackIPLiteral(t *testing.T) {
	for _, raw := range []string{
		"https://127.0.0.1/plugin.git",
		"https://[::1]/plugin.git",
	} {
		if _, err := ValidateCatalogRepositoryURL(context.Background(), raw); err == nil {
			t.Errorf("%q: want loopback rejected, got accepted", raw)
		}
	}
}

func TestValidateCatalogRepositoryURLRejectsLoopbackHostname(t *testing.T) {
	// "localhost" resolves via the OS's own resolver (no external network
	// dependency - every platform resolves it locally) to a loopback
	// address, so this proves hostname resolution is actually checked,
	// not just IP-literal string matching.
	if _, err := ValidateCatalogRepositoryURL(context.Background(), "https://localhost/plugin.git"); err == nil {
		t.Fatal("want localhost rejected")
	}
}

func TestValidateCatalogRepositoryURLRejectsPrivateIP(t *testing.T) {
	for _, raw := range []string{
		"https://10.0.0.5/plugin.git",
		"https://172.16.0.5/plugin.git",
		"https://192.168.1.5/plugin.git",
	} {
		if _, err := ValidateCatalogRepositoryURL(context.Background(), raw); err == nil {
			t.Errorf("%q: want private IP rejected, got accepted", raw)
		}
	}
}

func TestValidateCatalogRepositoryURLRejectsLinkLocalAndMetadata(t *testing.T) {
	for _, raw := range []string{
		"https://169.254.169.254/latest/meta-data/",
		"https://[fe80::1]/plugin.git",
		"https://[fd00:ec2::254]/plugin.git", // AWS IMDSv2 IPv6 - within the fc00::/7 private range
	} {
		if _, err := ValidateCatalogRepositoryURL(context.Background(), raw); err == nil {
			t.Errorf("%q: want link-local/metadata rejected, got accepted", raw)
		}
	}
}

func TestValidateCatalogRepositoryURLAcceptsSafeDestination(t *testing.T) {
	// 203.0.113.0/24 is the RFC 5737 TEST-NET-3 documentation range: a
	// real, valid, public (non-private, non-loopback, non-link-local) IP
	// literal chosen specifically so this test needs no real network
	// access or external DNS at all.
	got, err := ValidateCatalogRepositoryURL(context.Background(), "https://203.0.113.10/plugin.git")
	if err != nil {
		t.Fatalf("want a safe public destination accepted, got: %v", err)
	}
	if got != "https://203.0.113.10/plugin.git" {
		t.Fatalf("got %q", got)
	}
}

func TestCheckRedirectSafeStopsAfterTooManyHops(t *testing.T) {
	target, _ := http.NewRequest(http.MethodGet, "https://203.0.113.10/next", nil)
	var via []*http.Request
	for i := 0; i < maxCatalogRedirects; i++ {
		via = append(via, target)
	}
	if err := checkRedirectSafe(target, via); err == nil {
		t.Fatal("want redirect refused after the hop limit")
	}
}

func TestCheckRedirectSafeRejectsDowngradeToHTTP(t *testing.T) {
	target, _ := http.NewRequest(http.MethodGet, "http://203.0.113.10/next", nil)
	if err := checkRedirectSafe(target, nil); err == nil {
		t.Fatal("want a redirect to plain http refused")
	}
}

func TestCheckRedirectSafeRejectsPrivateTarget(t *testing.T) {
	target, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://169.254.169.254/latest/meta-data/", nil)
	if err := checkRedirectSafe(target, nil); err == nil {
		t.Fatal("want a redirect to a link-local/metadata target refused")
	}
}

// TestRedirectToPrivateTargetIsRefusedEndToEnd proves the wiring, not
// just the isolated function: a real httptest.Server issuing a 302 to a
// blocked destination must never be followed by a client configured
// with checkRedirectSafe. No real connection to the blocked address is
// attempted - CheckRedirect runs before Go's http.Client would dial it.
func TestRedirectToPrivateTargetIsRefusedEndToEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer server.Close()

	client := server.Client()
	client.CheckRedirect = checkRedirectSafe
	_, _, _, err := FetchCatalog(context.Background(), server.URL, client)
	if err == nil {
		t.Fatal("want the redirect to a blocked destination refused")
	}
	if !strings.Contains(err.Error(), "refused") && !strings.Contains(err.Error(), "not a permitted destination") {
		t.Fatalf("error should explain the refusal, got: %v", err)
	}
}

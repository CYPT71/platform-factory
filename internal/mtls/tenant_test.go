package mtls

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"net/url"
	"testing"
)

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestTenantIDRejectsNilCertificate(t *testing.T) {
	if got := TenantID(nil); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestTenantIDReadsOrganizationalUnitClaim(t *testing.T) {
	leaf := &x509.Certificate{Subject: pkix.Name{OrganizationalUnit: []string{"tenant:acme"}}}
	if got := TenantID(leaf); got != "acme" {
		t.Fatalf("got %q, want %q", got, "acme")
	}
}

func TestTenantIDTrimsOrganizationalUnitWhitespace(t *testing.T) {
	leaf := &x509.Certificate{Subject: pkix.Name{OrganizationalUnit: []string{"  tenant:acme  "}}}
	if got := TenantID(leaf); got != "acme" {
		t.Fatalf("got %q, want %q", got, "acme")
	}
}

func TestTenantIDRejectsOrganizationalUnitWithInvalidCharacters(t *testing.T) {
	leaf := &x509.Certificate{Subject: pkix.Name{OrganizationalUnit: []string{"tenant:ACME!"}}}
	if got := TenantID(leaf); got != "" {
		t.Fatalf("got %q, want empty for an uppercase/invalid tenant id", got)
	}
}

func TestTenantIDSkipsOrganizationalUnitWithoutTenantPrefix(t *testing.T) {
	leaf := &x509.Certificate{Subject: pkix.Name{OrganizationalUnit: []string{"engineering"}}}
	if got := TenantID(leaf); got != "" {
		t.Fatalf("got %q, want empty for an unrelated OU entry", got)
	}
}

func TestTenantIDReadsSPIFFEURISAN(t *testing.T) {
	leaf := &x509.Certificate{URIs: []*url.URL{mustParseURL(t, "spiffe://cluster.local/ns/default/tenant/acme")}}
	if got := TenantID(leaf); got != "acme" {
		t.Fatalf("got %q, want %q", got, "acme")
	}
}

func TestTenantIDReadsSPIFFEURISANWithTrailingSegment(t *testing.T) {
	leaf := &x509.Certificate{URIs: []*url.URL{mustParseURL(t, "spiffe://cluster.local/tenant/acme/worker-7")}}
	if got := TenantID(leaf); got != "acme" {
		t.Fatalf("got %q, want %q", got, "acme")
	}
}

func TestTenantIDRejectsSPIFFEURIWithInvalidTenantSegment(t *testing.T) {
	leaf := &x509.Certificate{URIs: []*url.URL{mustParseURL(t, "spiffe://cluster.local/tenant/ACME")}}
	if got := TenantID(leaf); got != "" {
		t.Fatalf("got %q, want empty for an uppercase SPIFFE tenant segment", got)
	}
}

func TestTenantIDIgnoresNonSPIFFEURI(t *testing.T) {
	leaf := &x509.Certificate{URIs: []*url.URL{mustParseURL(t, "https://cluster.local/tenant/acme")}}
	if got := TenantID(leaf); got != "" {
		t.Fatalf("got %q, want empty for a non-SPIFFE URI", got)
	}
}

func TestTenantIDIgnoresSPIFFEURIWithoutTenantPath(t *testing.T) {
	leaf := &x509.Certificate{URIs: []*url.URL{mustParseURL(t, "spiffe://cluster.local/ns/default/worker/worker-7")}}
	if got := TenantID(leaf); got != "" {
		t.Fatalf("got %q, want empty when the SPIFFE path has no /tenant/ segment", got)
	}
}

func TestTenantIDPrefersOrganizationalUnitOverSPIFFEURI(t *testing.T) {
	leaf := &x509.Certificate{
		Subject: pkix.Name{OrganizationalUnit: []string{"tenant:from-ou"}},
		URIs:    []*url.URL{mustParseURL(t, "spiffe://cluster.local/tenant/from-uri")},
	}
	if got := TenantID(leaf); got != "from-ou" {
		t.Fatalf("got %q, want %q", got, "from-ou")
	}
}

func TestTenantIDFallsBackToSPIFFEURIWhenOrganizationalUnitHasNoValidClaim(t *testing.T) {
	leaf := &x509.Certificate{
		Subject: pkix.Name{OrganizationalUnit: []string{"engineering"}},
		URIs:    []*url.URL{mustParseURL(t, "spiffe://cluster.local/tenant/from-uri")},
	}
	if got := TenantID(leaf); got != "from-uri" {
		t.Fatalf("got %q, want %q", got, "from-uri")
	}
}

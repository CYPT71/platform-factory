package license

import (
	"crypto/ed25519"
	"reflect"
	"testing"
	"time"

	"github.com/CYPT71/platform-factory/internal/attestation"
	"github.com/CYPT71/platform-factory/internal/signing"
)

func issuerStore(t *testing.T) (*signing.FileKeyStore, ed25519.PublicKey) {
	t.Helper()
	store, err := signing.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	public, err := store.PublicKey("vendor")
	if err != nil {
		t.Fatal(err)
	}
	return store, public
}

func TestIssueAndVerifyRoundTrip(t *testing.T) {
	store, public := issuerStore(t)
	issuedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expiresAt := issuedAt.AddDate(1, 0, 0)
	lic := License{
		LicenseID: "lic-001", Licensee: "Acme Corp", Edition: "enterprise",
		Seats: 10, Features: []string{"sso", "audit-log"},
		IssuedAt: issuedAt, ExpiresAt: expiresAt,
	}
	envelope, err := Issue(store, "vendor", "vendor-key", lic)
	if err != nil {
		t.Fatal(err)
	}
	trusted := map[string]ed25519.PublicKey{"vendor-key": public}
	now := issuedAt.AddDate(0, 6, 0)
	got, err := Verify(envelope, trusted, now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !reflect.DeepEqual(got, lic) {
		t.Fatalf("got=%+v, want=%+v", got, lic)
	}
	if !got.HasFeature("sso") || got.HasFeature("not-a-feature") {
		t.Fatalf("HasFeature mismatch: %+v", got)
	}
}

func TestVerifyRejectsExpiredLicense(t *testing.T) {
	store, public := issuerStore(t)
	issuedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	lic := License{
		LicenseID: "lic-002", Licensee: "Acme Corp", Edition: "enterprise",
		IssuedAt: issuedAt, ExpiresAt: issuedAt.AddDate(0, 1, 0),
	}
	envelope, err := Issue(store, "vendor", "vendor-key", lic)
	if err != nil {
		t.Fatal(err)
	}
	trusted := map[string]ed25519.PublicKey{"vendor-key": public}
	afterExpiry := lic.ExpiresAt.AddDate(0, 0, 1)
	if _, err := Verify(envelope, trusted, afterExpiry); err == nil {
		t.Fatal("expired license verified")
	}
}

func TestLicenseWithoutExpiryNeverExpires(t *testing.T) {
	store, public := issuerStore(t)
	lic := License{
		LicenseID: "lic-003", Licensee: "Acme Corp", Edition: "support",
		IssuedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	envelope, err := Issue(store, "vendor", "vendor-key", lic)
	if err != nil {
		t.Fatal(err)
	}
	trusted := map[string]ed25519.PublicKey{"vendor-key": public}
	farFuture := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := Verify(envelope, trusted, farFuture); err != nil {
		t.Fatalf("license with no expiry rejected: %v", err)
	}
}

func TestVerifyRejectsUntrustedSignerAndTampering(t *testing.T) {
	store, _ := issuerStore(t)
	_, otherPublic := issuerStore(t)
	lic := License{
		LicenseID: "lic-004", Licensee: "Acme Corp", Edition: "enterprise",
		IssuedAt: time.Now().UTC(),
	}
	envelope, err := Issue(store, "vendor", "vendor-key", lic)
	if err != nil {
		t.Fatal(err)
	}

	// A different signer's public key must not validate this envelope.
	untrusted := map[string]ed25519.PublicKey{"vendor-key": otherPublic}
	if _, err := Verify(envelope, untrusted, time.Now()); err == nil {
		t.Fatal("verified against the wrong signer's public key")
	}

	// Tampering the signed payload must invalidate the signature, even
	// against the correct signer's own public key.
	store2, public2 := issuerStore(t)
	envelope2, err := Issue(store2, "vendor", "vendor-key", lic)
	if err != nil {
		t.Fatal(err)
	}
	envelope2.Payload += "A"
	if _, err := Verify(envelope2, map[string]ed25519.PublicKey{"vendor-key": public2}, time.Now()); err == nil {
		t.Fatal("tampered license envelope verified")
	}
}

func TestVerifyRejectsWrongPayloadType(t *testing.T) {
	store, public := issuerStore(t)
	envelope, err := attestation.Sign(store, "vendor", "vendor-key", "application/vnd.other.v1+json",
		map[string]string{"not": "a license"})
	if err != nil {
		t.Fatal(err)
	}
	trusted := map[string]ed25519.PublicKey{"vendor-key": public}
	if _, err := Verify(envelope, trusted, time.Now()); err == nil {
		t.Fatal("verified an envelope of the wrong payload type")
	}
}

func TestLicenseValidateRejectsMissingRequiredFields(t *testing.T) {
	base := License{LicenseID: "lic-005", Licensee: "Acme", Edition: "enterprise", IssuedAt: time.Now()}
	if err := base.Validate(); err != nil {
		t.Fatalf("expected a well-formed license to validate, got %v", err)
	}
	cases := []License{
		{Licensee: "Acme", Edition: "enterprise", IssuedAt: time.Now()},
		{LicenseID: "lic-005", Edition: "enterprise", IssuedAt: time.Now()},
		{LicenseID: "lic-005", Licensee: "Acme", IssuedAt: time.Now()},
		{LicenseID: "lic-005", Licensee: "Acme", Edition: "enterprise"},
		{LicenseID: "lic-005", Licensee: "Acme", Edition: "enterprise", Seats: -1, IssuedAt: time.Now()},
		{
			LicenseID: "lic-005", Licensee: "Acme", Edition: "enterprise",
			IssuedAt: time.Now(), ExpiresAt: time.Now().Add(-time.Hour),
		},
	}
	for i, c := range cases {
		if err := c.Validate(); err == nil {
			t.Fatalf("case %d: expected a validation error for %+v", i, c)
		}
	}
}

func TestIssueRejectsAnInvalidLicense(t *testing.T) {
	store, _ := issuerStore(t)
	if _, err := Issue(store, "vendor", "vendor-key", License{}); err == nil {
		t.Fatal("expected Issue to reject an empty license before signing it")
	}
}

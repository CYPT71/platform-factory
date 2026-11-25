package license

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CYPT71/platform-factory/internal/attestation"
	"github.com/CYPT71/platform-factory/internal/signing"
)

// PayloadType identifies a License predicate inside its DSSE envelope -
// Verify refuses an otherwise-valid envelope carrying any other type, the
// same cross-type confusion guard every other predicate in this codebase
// (provenance, SBOM, release subjects) already applies.
const PayloadType = "application/vnd.platform-factory.license.v1+json"

// License is the signed predicate for one issued commercial license. It
// is deliberately small: what it grants (Edition, Features, Seats) and
// for how long (IssuedAt, ExpiresAt) - not a general-purpose entitlements
// model, since nothing in this codebase gates a capability on one yet
// (see the package doc comment).
type License struct {
	// LicenseID is this license's own stable identifier (not secret -
	// it appears in the signed, public envelope), for revocation lists
	// or support lookups to key off without parsing Licensee text.
	LicenseID string `json:"license_id"`
	// Licensee names who this license was issued to. Free text (a
	// company or individual name), not an authentication credential -
	// the signature is what makes a license trustworthy, not this field.
	Licensee string `json:"licensee"`
	// Edition names the commercial offering this license grants (for
	// example "enterprise", "support", "hosting-exception") - see
	// docs/legal/pricing.md for what, if anything, these mean today.
	Edition string `json:"edition"`
	// Seats is the licensed seat/instance count. Zero means unlimited -
	// a deliberate choice (there is no separate "unlimited" sentinel to
	// keep in sync with this one), so a license that means to cap usage
	// must set a positive Seats value explicitly.
	Seats int `json:"seats,omitempty"`
	// Features lists the specific gated capabilities this license
	// unlocks. Empty means the Edition's default set, whatever a future
	// caller decides that is - this package does not define what any
	// feature name does.
	Features []string  `json:"features,omitempty"`
	IssuedAt time.Time `json:"issued_at"`
	// ExpiresAt is this license's expiry. Zero means it never expires -
	// Verify treats a zero value as "no expiry", not as already-expired.
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// Validate checks License's own fields for internal consistency,
// independent of whether it is signed or expired.
func (l License) Validate() error {
	if l.LicenseID == "" {
		return errors.New("license: license_id is required")
	}
	if l.Licensee == "" {
		return errors.New("license: licensee is required")
	}
	if l.Edition == "" {
		return errors.New("license: edition is required")
	}
	if l.Seats < 0 {
		return errors.New("license: seats must not be negative")
	}
	if l.IssuedAt.IsZero() {
		return errors.New("license: issued_at is required")
	}
	if !l.ExpiresAt.IsZero() && l.ExpiresAt.Before(l.IssuedAt) {
		return errors.New("license: expires_at precedes issued_at")
	}
	return nil
}

// HasFeature reports whether name is explicitly listed in Features.
func (l License) HasFeature(name string) bool {
	for _, feature := range l.Features {
		if feature == name {
			return true
		}
	}
	return false
}

// ExpiredAt reports whether l had already expired at now. A zero
// ExpiresAt (no expiry) is never expired.
func (l License) ExpiredAt(now time.Time) bool {
	return !l.ExpiresAt.IsZero() && now.After(l.ExpiresAt)
}

// Issue signs lic into a DSSE envelope using store's named key, the same
// attestation.Sign every other signed predicate in this codebase already
// uses. keyID must match the public key the eventual Verify caller trusts
// under that ID (see attestation.Verify's keys map).
func Issue(store signing.KeyStore, keyName, keyID string, lic License) (attestation.Envelope, error) {
	if err := lic.Validate(); err != nil {
		return attestation.Envelope{}, err
	}
	return attestation.Sign(store, keyName, keyID, PayloadType, lic)
}

// Verify authenticates envelope against trustedKeys, rejects a wrong
// PayloadType or a structurally invalid predicate, and returns the
// enclosed License only if it has not expired as of now. Callers that
// need to inspect an expired license (to report when expiry happened)
// should decode envelope.Payload directly rather than relying on this
// function's error.
func Verify(envelope attestation.Envelope, trustedKeys map[string]ed25519.PublicKey, now time.Time) (License, error) {
	if envelope.PayloadType != PayloadType {
		return License{}, fmt.Errorf("license: unexpected payload type %q, want %q", envelope.PayloadType, PayloadType)
	}
	payload, err := attestation.Verify(envelope, trustedKeys)
	if err != nil {
		return License{}, err
	}
	var lic License
	if err := json.Unmarshal(payload, &lic); err != nil {
		return License{}, fmt.Errorf("license: decode predicate: %w", err)
	}
	if err := lic.Validate(); err != nil {
		return License{}, err
	}
	if lic.ExpiredAt(now) {
		return License{}, fmt.Errorf("license: %q expired at %s (now %s)", lic.LicenseID, lic.ExpiresAt.Format(time.RFC3339), now.Format(time.RFC3339))
	}
	return lic, nil
}

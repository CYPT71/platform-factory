package attestation

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/signing"
)

// FuzzVerify feeds arbitrary envelope JSON at Verify against one real
// trusted key. This is the exact attack surface secure-oci verify-release
// and secure-oci publish's signature checks depend on: an attacker fully
// controls the envelope bytes (a file on disk, or in the future a
// registry-fetched artifact), and Verify must never panic, only
// authenticate or reject.
func FuzzVerify(f *testing.F) {
	store, err := signing.NewFileKeyStore(f.TempDir())
	if err != nil {
		f.Fatal(err)
	}
	publicKey, err := store.PublicKey("release")
	if err != nil {
		f.Fatal(err)
	}
	valid, err := Sign(store, "release", "release-key", "application/vnd.secure-oci.subject.v1+json",
		map[string]string{"digest": "sha256:00"})
	if err != nil {
		f.Fatal(err)
	}
	validJSON, err := json.Marshal(valid)
	if err != nil {
		f.Fatal(err)
	}
	keys := map[string]ed25519.PublicKey{"release-key": publicKey}

	f.Add(string(validJSON))
	f.Add(`{}`)
	f.Add(`{"payloadType":"x","payload":"not-base64!!","signatures":[{"keyid":"release-key","sig":"AA=="}]}`)
	f.Add(`{"payloadType":"","payload":"","signatures":[]}`)
	f.Add(`{"payloadType":"x","payload":"AA==","signatures":[{"keyid":"unknown","sig":"AA=="}]}`)
	f.Add(`null`)

	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 1<<16 {
			t.Skip()
		}
		var envelope Envelope
		if json.Unmarshal([]byte(raw), &envelope) != nil {
			return
		}
		_, _ = Verify(envelope, keys)
	})
}

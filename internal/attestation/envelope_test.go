package attestation

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"

	"github.com/CYPT71/platform-factory/internal/signing"
)

func TestEnvelopeRoundTripAndTamperRejection(t *testing.T) {
	store, err := signing.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := Sign(store, "release", "release-key", "https://platform-factory.dev/predicate/v1",
		map[string]string{"subject": "sha256:abc"})
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := store.PublicKey("release")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := Verify(envelope, map[string]ed25519.PublicKey{"release-key": publicKey})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded["subject"] != "sha256:abc" {
		t.Fatalf("payload=%s err=%v", payload, err)
	}
	envelope.Payload += "A"
	if _, err := Verify(envelope, map[string]ed25519.PublicKey{"release-key": publicKey}); err == nil {
		t.Fatal("tampered envelope verified")
	}
}

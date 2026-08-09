//go:build darwin && cgo

package signing

import (
	"bytes"
	"path/filepath"
	"testing"
)

// newTestKeychain always creates a fresh, ephemeral keychain file under
// t.TempDir() — never the user's real login keychain — so these tests are
// safe to run non-interactively and leave nothing behind.
func newTestKeychain(t *testing.T) KeyStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "signing-test.keychain")
	store, err := NewKeychainKeyStore(path, "test-password")
	if err != nil {
		t.Fatalf("new keychain store: %v", err)
	}
	return store
}

func TestKeychainKeyStoreGeneratesAndPersistsKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signing-test.keychain")
	store, err := NewKeychainKeyStore(path, "test-password")
	if err != nil {
		t.Fatalf("new keychain store: %v", err)
	}
	first, err := store.PublicKey("release")
	if err != nil {
		t.Fatalf("public key: %v", err)
	}

	// Re-opening the same keychain file must load the persisted key.
	store2, err := NewKeychainKeyStore(path, "test-password")
	if err != nil {
		t.Fatalf("reopen keychain store: %v", err)
	}
	second, err := store2.PublicKey("release")
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("public key changed across keychain reopen")
	}
}

func TestKeychainKeyStoreDistinctNamesGetDistinctKeys(t *testing.T) {
	store := newTestKeychain(t)
	a, err := store.PublicKey("release")
	if err != nil {
		t.Fatalf("public key a: %v", err)
	}
	b, err := store.PublicKey("staging")
	if err != nil {
		t.Fatalf("public key b: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("distinct key names produced the same key")
	}
}

func TestKeychainKeyStoreSignAndVerifyRoundTrip(t *testing.T) {
	store := newTestKeychain(t)
	message := []byte("build subject digest")
	signature, err := store.Sign("release", message)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	publicKey, err := store.PublicKey("release")
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	if err := Verify(publicKey, message, signature); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestKeychainKeyStoreVerifyRejectsTamperedSignature(t *testing.T) {
	store := newTestKeychain(t)
	message := []byte("build subject digest")
	signature, err := store.Sign("release", message)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	publicKey, err := store.PublicKey("release")
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	tampered := append([]byte(nil), signature...)
	tampered[0] ^= 0xFF
	if err := Verify(publicKey, message, tampered); err == nil {
		t.Fatal("expected verification to fail for a tampered signature")
	}
}

func TestKeychainKeyStoreRejectsInvalidName(t *testing.T) {
	store := newTestKeychain(t)
	for _, name := range []string{"", "Bad_Name", "../escape"} {
		if _, err := store.PublicKey(name); err == nil {
			t.Fatalf("PublicKey(%q) accepted an invalid name", name)
		}
	}
}

func TestNewKeychainKeyStoreRejectsEmptyArgs(t *testing.T) {
	if _, err := NewKeychainKeyStore("", "password"); err == nil {
		t.Fatal("expected an error for an empty path")
	}
	if _, err := NewKeychainKeyStore(filepath.Join(t.TempDir(), "x.keychain"), ""); err == nil {
		t.Fatal("expected an error for an empty password")
	}
}

package signing

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestFileKeyStoreGeneratesAndPersistsKey(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileKeyStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	first, err := store.PublicKey("release")
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "release.key")); err != nil {
		t.Fatalf("expected key file to be written: %v", err)
	}

	// A fresh store instance over the same directory must load the
	// persisted key, not generate a new one.
	store2, err := NewFileKeyStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	second, err := store2.PublicKey("release")
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("public key changed across store instances")
	}
}

func TestFileKeyStoreDistinctNamesGetDistinctKeys(t *testing.T) {
	store, err := NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
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

func TestFileKeyStoreSignAndVerifyRoundTrip(t *testing.T) {
	store, err := NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
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

func TestVerifyRejectsTamperedInputs(t *testing.T) {
	store, err := NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	message := []byte("build subject digest")
	signature, err := store.Sign("release", message)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	publicKey, err := store.PublicKey("release")
	if err != nil {
		t.Fatalf("public key: %v", err)
	}

	if err := Verify(publicKey, []byte("different message"), signature); err == nil {
		t.Fatal("expected verification to fail for a tampered message")
	}
	tampered := append([]byte(nil), signature...)
	tampered[0] ^= 0xFF
	if err := Verify(publicKey, message, tampered); err == nil {
		t.Fatal("expected verification to fail for a tampered signature")
	}
	other, err := store.PublicKey("staging")
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	if err := Verify(other, message, signature); err == nil {
		t.Fatal("expected verification to fail under the wrong public key")
	}
}

func TestVerifyRejectsMalformedPublicKey(t *testing.T) {
	if err := Verify([]byte("too short"), []byte("m"), []byte("s")); err == nil {
		t.Fatal("expected an error for a malformed public key")
	}
}

func TestFileKeyStoreRejectsInvalidName(t *testing.T) {
	store, err := NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	for _, name := range []string{"", "Bad_Name", "../escape", "a/b"} {
		if _, err := store.PublicKey(name); err == nil {
			t.Fatalf("PublicKey(%q) accepted an invalid name", name)
		}
		if _, err := store.Sign(name, []byte("m")); err == nil {
			t.Fatalf("Sign(%q) accepted an invalid name", name)
		}
	}
}

func TestFileKeyStoreRejectsCorruptKeyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "release.key"), []byte("not a pem key"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	store, err := NewFileKeyStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.PublicKey("release"); err == nil {
		t.Fatal("expected an error for a corrupt key file")
	}
}

func TestFileKeyStoreConcurrentSignIsSafe(t *testing.T) {
	store, err := NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = store.Sign("release", []byte("message"))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("sign[%d]: %v", i, err)
		}
	}
}

func TestNewFileKeyStoreRejectsUncreatableDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(root, []byte("x"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := NewFileKeyStore(filepath.Join(root, "keys")); err == nil {
		t.Fatal("expected an error when the parent path is a file")
	}
}

package signing

import (
	"crypto/elliptic"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

func TestFileKeyStoreECDSAGeneratesAndPersistsKey(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileKeyStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	first, err := store.ECDSAPublicKey("release")
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	if first.Curve != elliptic.P256() {
		t.Fatalf("curve=%v", first.Curve)
	}
	if _, err := os.Stat(filepath.Join(dir, "release.ecdsa.key")); err != nil {
		t.Fatalf("expected key file: %v", err)
	}

	store2, err := NewFileKeyStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	second, err := store2.ECDSAPublicKey("release")
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	if !first.Equal(second) {
		t.Fatal("public key changed across store instances")
	}
}

func TestFileKeyStoreECDSAAndEd25519AreDistinctNamespaces(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileKeyStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.PublicKey("release"); err != nil {
		t.Fatalf("ed25519 public key: %v", err)
	}
	if _, err := store.ECDSAPublicKey("release"); err != nil {
		t.Fatalf("ecdsa public key: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "release.key")); err != nil {
		t.Fatalf("expected ed25519 key file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "release.ecdsa.key")); err != nil {
		t.Fatalf("expected ecdsa key file: %v", err)
	}
}

func TestFileKeyStoreSignECDSAVerifiesAgainstCertificate(t *testing.T) {
	// SignECDSA's convention (SHA-256 digest, ASN.1 signature) must match
	// VerifySignedByCertificate's ECDSA path exactly, since a real signer
	// would use this to produce signatures a certificate-holding verifier
	// checks.
	dir := t.TempDir()
	store, err := NewFileKeyStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	message := []byte("build subject digest")
	signature, err := store.SignECDSA("release", message)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	publicKey, err := store.ECDSAPublicKey("release")
	if err != nil {
		t.Fatalf("public key: %v", err)
	}

	cert := &x509.Certificate{PublicKey: publicKey}
	if err := VerifySignedByCertificate(cert, message, signature); err != nil {
		t.Fatalf("verify: %v", err)
	}
	tampered := append([]byte(nil), signature...)
	tampered[len(tampered)-1] ^= 0xFF
	if err := VerifySignedByCertificate(cert, message, tampered); err == nil {
		t.Fatal("expected verification to fail for a tampered signature")
	}
}

func TestFileKeyStoreECDSARejectsInvalidName(t *testing.T) {
	store, err := NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.ECDSAPublicKey("Bad_Name"); err == nil {
		t.Fatal("expected an error for an invalid name")
	}
	if _, err := store.SignECDSA("../escape", []byte("m")); err == nil {
		t.Fatal("expected an error for an invalid name")
	}
}

func TestFileKeyStoreECDSARejectsCorruptKeyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "release.ecdsa.key"), []byte("not a pem key"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	store, err := NewFileKeyStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.ECDSAPublicKey("release"); err == nil {
		t.Fatal("expected an error for a corrupt key file")
	}
}

func TestFileKeyStoreECDSARejectsEd25519KeyFile(t *testing.T) {
	// A .ecdsa.key file that actually contains an Ed25519 key (e.g. from a
	// hand-edited or corrupted file) must be rejected, not silently
	// misread.
	dir := t.TempDir()
	store, err := NewFileKeyStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.PublicKey("release"); err != nil {
		t.Fatalf("ed25519 public key: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "release.key"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release.ecdsa.key"), data, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := store.ECDSAPublicKey("release"); err == nil {
		t.Fatal("expected an error for an Ed25519 key in the ECDSA namespace")
	}
}

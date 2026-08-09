package signing

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ECDSAPublicKey returns the P-256 ECDSA public key for name, generating
// and persisting a new keypair on first use. This is a distinct key
// namespace from PublicKey/Sign's Ed25519 keys: the same name may have
// both an Ed25519 and an ECDSA key.
func (f *FileKeyStore) ECDSAPublicKey(name string) (*ecdsa.PublicKey, error) {
	priv, err := f.loadOrGenerateECDSA(name)
	if err != nil {
		return nil, err
	}
	return &priv.PublicKey, nil
}

// SignECDSA signs a SHA-256 digest of message with name's ECDSA private
// key, generating one on first use. This matches
// VerifySignedByCertificate's ECDSA convention (SHA-256 digest, ASN.1
// signature).
func (f *FileKeyStore) SignECDSA(name string, message []byte) ([]byte, error) {
	priv, err := f.loadOrGenerateECDSA(name)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(message)
	return ecdsa.SignASN1(rand.Reader, priv, digest[:])
}

func (f *FileKeyStore) loadOrGenerateECDSA(name string) (*ecdsa.PrivateKey, error) {
	if !namePattern.MatchString(name) {
		return nil, fmt.Errorf("signing: invalid key name %q", name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	path := filepath.Join(f.dir, name+".ecdsa.key")
	data, err := os.ReadFile(path)
	if err == nil {
		return decodeECDSAPrivateKey(data)
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("signing: read key %q: %w", name, err)
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("signing: generate key %q: %w", name, err)
	}
	encoded, err := encodePrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("signing: encode key %q: %w", name, err)
	}
	if err := atomicWriteKey(f.dir, name+".ecdsa.key", encoded); err != nil {
		return nil, fmt.Errorf("signing: write key %q: %w", name, err)
	}
	return priv, nil
}

func decodeECDSAPrivateKey(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("signing: invalid key file")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("signing: parse key: %w", err)
	}
	priv, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("signing: key file does not contain an ECDSA key")
	}
	return priv, nil
}

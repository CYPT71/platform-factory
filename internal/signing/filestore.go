package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// FileKeyStore stores Ed25519 private keys as PEM-encoded PKCS8 files
// under a directory, one file per name, generated on first use.
type FileKeyStore struct {
	dir string
	mu  sync.Mutex
}

// NewFileKeyStore creates (if necessary) dir and returns a store rooted
// there.
func NewFileKeyStore(dir string) (*FileKeyStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("signing: %w", err)
	}
	return &FileKeyStore{dir: dir}, nil
}

// PublicKey implements KeyStore.
func (f *FileKeyStore) PublicKey(name string) (ed25519.PublicKey, error) {
	priv, err := f.loadOrGenerate(name)
	if err != nil {
		return nil, err
	}
	public, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("signing: unexpected public key type")
	}
	return public, nil
}

// Sign implements KeyStore.
func (f *FileKeyStore) Sign(name string, message []byte) ([]byte, error) {
	priv, err := f.loadOrGenerate(name)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(priv, message), nil
}

func (f *FileKeyStore) loadOrGenerate(name string) (ed25519.PrivateKey, error) {
	if !namePattern.MatchString(name) {
		return nil, fmt.Errorf("signing: invalid key name %q", name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	path := filepath.Join(f.dir, name+".key")
	data, err := os.ReadFile(path)
	if err == nil {
		return decodePrivateKey(data)
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("signing: read key %q: %w", name, err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("signing: generate key %q: %w", name, err)
	}
	encoded, err := encodePrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("signing: encode key %q: %w", name, err)
	}
	if err := atomicWriteKey(f.dir, name+".key", encoded); err != nil {
		return nil, fmt.Errorf("signing: write key %q: %w", name, err)
	}
	return priv, nil
}

// encodePrivateKey PKCS8/PEM-encodes any key type x509.MarshalPKCS8PrivateKey
// supports (used for both Ed25519 and ECDSA keys).
func encodePrivateKey(key any) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func decodePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("signing: invalid key file")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("signing: parse key: %w", err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("signing: key file does not contain an Ed25519 key")
	}
	return priv, nil
}

func atomicWriteKey(dir, name string, data []byte) error {
	temporary, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	success := false
	defer func() {
		_ = temporary.Close()
		if !success {
			_ = os.Remove(temporary.Name())
		}
	}()
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temporary.Name(), 0600); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	success = true
	return nil
}

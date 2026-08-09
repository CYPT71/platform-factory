// Package signing signs build artifacts with Ed25519 and verifies those
// signatures, using Go's native crypto/ed25519 rather than an external
// signing CLI. Key storage is pluggable behind KeyStore: internal/signing
// ships a portable local-file backend (filestore.go) and a macOS Keychain
// backend (keychain_darwin.go, only on darwin with cgo enabled).
package signing

import (
	"crypto/ed25519"
	"errors"
)

// KeyStore manages named Ed25519 signing keys.
type KeyStore interface {
	// PublicKey returns the public key for name, generating and
	// persisting a new keypair on first use.
	PublicKey(name string) (ed25519.PublicKey, error)
	// Sign returns a detached signature over message using name's
	// private key.
	Sign(name string, message []byte) ([]byte, error)
}

// Verify checks that signature is a valid Ed25519 signature of message
// under publicKey. It has no KeyStore dependency: publicKey may come from
// any source, including a peer's published key.
func Verify(publicKey ed25519.PublicKey, message, signature []byte) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("signing: invalid public key size")
	}
	if !ed25519.Verify(publicKey, message, signature) {
		return errors.New("signing: signature verification failed")
	}
	return nil
}

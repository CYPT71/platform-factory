package signing

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
)

// VerifyChain validates leaf against intermediates and roots using Go's
// native X.509 chain verification (crypto/x509.Certificate.Verify — no
// external command), returning the valid chain(s) found. If opts.Roots or
// opts.Intermediates is nil, it is built from the roots/intermediates
// arguments.
func VerifyChain(leaf *x509.Certificate, intermediates, roots []*x509.Certificate, opts x509.VerifyOptions) ([][]*x509.Certificate, error) {
	if leaf == nil {
		return nil, errors.New("signing: leaf certificate is required")
	}
	if opts.Roots == nil {
		pool := x509.NewCertPool()
		for _, cert := range roots {
			pool.AddCert(cert)
		}
		opts.Roots = pool
	}
	if opts.Intermediates == nil {
		pool := x509.NewCertPool()
		for _, cert := range intermediates {
			pool.AddCert(cert)
		}
		opts.Intermediates = pool
	}
	chains, err := leaf.Verify(opts)
	if err != nil {
		return nil, fmt.Errorf("signing: verify certificate chain: %w", err)
	}
	return chains, nil
}

// VerifySignedByCertificate verifies signature over message was produced
// by cert's private key. Ed25519 keys sign the raw message, matching
// FileKeyStore/KeychainKeyStore's convention; ECDSA keys sign a SHA-256
// digest of message. This checks only the signature, not the
// certificate's validity or trust — call VerifyChain first.
func VerifySignedByCertificate(cert *x509.Certificate, message, signature []byte) error {
	switch key := cert.PublicKey.(type) {
	case ed25519.PublicKey:
		return Verify(key, message, signature)
	case *ecdsa.PublicKey:
		digest := sha256.Sum256(message)
		if !ecdsa.VerifyASN1(key, digest[:], signature) {
			return errors.New("signing: ECDSA signature verification failed")
		}
		return nil
	default:
		return fmt.Errorf("signing: unsupported certificate public key type %T", cert.PublicKey)
	}
}

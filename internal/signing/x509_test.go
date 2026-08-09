package signing

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

type testChain struct {
	root         *x509.Certificate
	intermediate *x509.Certificate
	leaf         *x509.Certificate
	leafKey      crypto.Signer
}

// buildTestChain builds a fresh root -> intermediate -> leaf X.509 chain,
// with the leaf using leafAlgorithm ("ed25519" or "ecdsa").
func buildTestChain(t *testing.T, leafAlgorithm string) testChain {
	t.Helper()
	now := time.Now()

	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("root key: %v", err)
	}
	rootTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "root"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IsCA:     true, BasicConstraintsValid: true,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatalf("parse root: %v", err)
	}

	intermediateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("intermediate key: %v", err)
	}
	intermediateTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "intermediate"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IsCA:     true, BasicConstraintsValid: true,
	}
	intermediateDER, err := x509.CreateCertificate(rand.Reader, intermediateTemplate, root, &intermediateKey.PublicKey, rootKey)
	if err != nil {
		t.Fatalf("create intermediate: %v", err)
	}
	intermediate, err := x509.ParseCertificate(intermediateDER)
	if err != nil {
		t.Fatalf("parse intermediate: %v", err)
	}

	var leafPub crypto.PublicKey
	var leafPriv crypto.Signer
	switch leafAlgorithm {
	case "ed25519":
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("leaf key: %v", err)
		}
		leafPub, leafPriv = pub, priv
	case "ecdsa":
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("leaf key: %v", err)
		}
		leafPub, leafPriv = &priv.PublicKey, priv
	default:
		t.Fatalf("unknown leaf algorithm %q", leafAlgorithm)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "leaf"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, intermediate, leafPub, intermediateKey)
	if err != nil {
		t.Fatalf("create leaf: %v", err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}

	return testChain{root: root, intermediate: intermediate, leaf: leaf, leafKey: leafPriv}
}

func TestVerifyChainAcceptsValidChain(t *testing.T) {
	chain := buildTestChain(t, "ecdsa")
	chains, err := VerifyChain(chain.leaf, []*x509.Certificate{chain.intermediate}, []*x509.Certificate{chain.root}, x509.VerifyOptions{})
	if err != nil {
		t.Fatalf("verify chain: %v", err)
	}
	if len(chains) == 0 {
		t.Fatal("expected at least one valid chain")
	}
}

func TestVerifyChainRejectsUntrustedRoot(t *testing.T) {
	chain := buildTestChain(t, "ecdsa")
	other := buildTestChain(t, "ecdsa")
	_, err := VerifyChain(chain.leaf, []*x509.Certificate{chain.intermediate}, []*x509.Certificate{other.root}, x509.VerifyOptions{})
	if err == nil {
		t.Fatal("expected verification to fail against an unrelated root")
	}
}

func TestVerifyChainRejectsMissingIntermediate(t *testing.T) {
	chain := buildTestChain(t, "ecdsa")
	_, err := VerifyChain(chain.leaf, nil, []*x509.Certificate{chain.root}, x509.VerifyOptions{})
	if err == nil {
		t.Fatal("expected verification to fail without the intermediate that actually issued the leaf")
	}
}

func TestVerifyChainRequiresLeaf(t *testing.T) {
	if _, err := VerifyChain(nil, nil, nil, x509.VerifyOptions{}); err == nil {
		t.Fatal("expected an error for a nil leaf certificate")
	}
}

func TestVerifySignedByCertificateEd25519(t *testing.T) {
	chain := buildTestChain(t, "ed25519")
	message := []byte("build subject digest")
	signature := ed25519.Sign(chain.leafKey.(ed25519.PrivateKey), message)

	if err := VerifySignedByCertificate(chain.leaf, message, signature); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := VerifySignedByCertificate(chain.leaf, []byte("different"), signature); err == nil {
		t.Fatal("expected verification to fail for a tampered message")
	}
}

func TestVerifySignedByCertificateECDSA(t *testing.T) {
	chain := buildTestChain(t, "ecdsa")
	message := []byte("build subject digest")
	digest := sha256.Sum256(message)
	signature, err := ecdsa.SignASN1(rand.Reader, chain.leafKey.(*ecdsa.PrivateKey), digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	if err := VerifySignedByCertificate(chain.leaf, message, signature); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := VerifySignedByCertificate(chain.leaf, []byte("different"), signature); err == nil {
		t.Fatal("expected verification to fail for a tampered message")
	}
}

func TestVerifySignedByCertificateRejectsUnsupportedKeyType(t *testing.T) {
	cert := &x509.Certificate{PublicKey: "not a real key"}
	if err := VerifySignedByCertificate(cert, []byte("m"), []byte("s")); err == nil {
		t.Fatal("expected an error for an unsupported public key type")
	}
}

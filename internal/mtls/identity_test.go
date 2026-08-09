package mtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

// issueLeafWithOrganization is a parallel to handshake_test.go's issueLeaf
// that also sets Subject.Organization, for tests specifically about the
// role a certificate declares rather than the peer identity its
// CommonName gives.
func issueLeafWithOrganization(t *testing.T, ca issuedCertificate, commonName string, organization []string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: commonName, Organization: organization},
		NotBefore:    time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.caCert, &key.PublicKey, ca.caKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestHasRoleMatchesDeclaredOrganization(t *testing.T) {
	ca := newTestCA(t)
	worker := issueLeafWithOrganization(t, ca, "worker-7", []string{"worker"})
	if !HasRole(worker, "worker") {
		t.Fatal("worker certificate did not match its own declared role")
	}
	if HasRole(worker, "operator") {
		t.Fatal("worker certificate matched an unrelated role")
	}
}

func TestHasRoleRejectsMissingOrganization(t *testing.T) {
	ca := newTestCA(t)
	bare := issueLeafWithOrganization(t, ca, "worker-7", nil)
	if HasRole(bare, "worker") {
		t.Fatal("certificate with no Organization matched a role")
	}
}

func TestHasRoleRejectsMultipleOrganizationsCorrectly(t *testing.T) {
	ca := newTestCA(t)
	multi := issueLeafWithOrganization(t, ca, "worker-7", []string{"internal-tools", "worker"})
	if !HasRole(multi, "worker") {
		t.Fatal("certificate declaring worker among several organizations did not match")
	}
	if HasRole(multi, "operator") {
		t.Fatal("certificate matched a role it never declared")
	}
}

func TestHasRoleRejectsNilCertificate(t *testing.T) {
	if HasRole(nil, "worker") {
		t.Fatal("nil certificate matched a role")
	}
}

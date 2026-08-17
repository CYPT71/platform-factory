package mtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"
)

// issuedCertificate is a CA-signed leaf certificate plus the PEM bundle of
// the CA that signed it, everything a real ClientConfig/ServerConfig needs.
type issuedCertificate struct {
	caPEM  []byte
	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
}

func newTestCA(t *testing.T) issuedCertificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return issuedCertificate{
		caPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		caCert: cert, caKey: key,
	}
}

// issueLeaf signs commonName with ca, producing a certificate ClientConfig
// or ServerConfig can present.
func issueLeaf(t *testing.T, ca issuedCertificate, commonName string, dnsNames []string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: commonName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		DNSNames:    dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.caCert, &key.PublicKey, ca.caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// serveOnce accepts exactly one TLS connection with serverConfig, reads one
// byte, and reports the negotiated peer identity (or the handshake error)
// on result.
type handshakeResult struct {
	peerCommonName string
	err            error
}

func serveOnce(t *testing.T, serverConfig *tls.Config) (addr string, result <-chan handshakeResult) {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	out := make(chan handshakeResult, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			out <- handshakeResult{err: err}
			return
		}
		defer conn.Close()
		tlsConn, ok := conn.(*tls.Conn)
		if !ok {
			out <- handshakeResult{err: errors.New("accepted connection is not TLS")}
			return
		}
		if err := tlsConn.Handshake(); err != nil {
			out <- handshakeResult{err: err}
			return
		}
		state := tlsConn.ConnectionState()
		commonName := ""
		if len(state.PeerCertificates) > 0 {
			commonName = state.PeerCertificates[0].Subject.CommonName
		}
		buf := make([]byte, 1)
		if _, err := conn.Read(buf); err != nil {
			out <- handshakeResult{err: err}
			return
		}
		out <- handshakeResult{peerCommonName: commonName}
	}()
	return listener.Addr().String(), out
}

// TestMutualHandshakeAuthenticatesBothPeers performs a live mutual TLS handshake.
func TestMutualHandshakeAuthenticatesBothPeers(t *testing.T) {
	ca := newTestCA(t)
	serverCert := issueLeaf(t, ca, "control-plane", []string{"control-plane.internal"})
	clientCert := issueLeaf(t, ca, "worker-7", nil)

	serverConfig, err := ServerConfig(Options{Certificates: []tls.Certificate{serverCert}, CAPEM: ca.caPEM, MutualTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	addr, results := serveOnce(t, serverConfig)

	clientConfig, err := ClientConfig(Options{
		Certificates: []tls.Certificate{clientCert}, CAPEM: ca.caPEM, ServerName: "control-plane.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := tls.Dial("tcp", addr, clientConfig)
	if err != nil {
		t.Fatalf("client-side handshake: %v", err)
	}
	defer conn.Close()
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 || state.PeerCertificates[0].Subject.CommonName != "control-plane" {
		t.Fatalf("client did not verify the server's identity: %+v", state.PeerCertificates)
	}
	if _, err := conn.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}

	select {
	case result := <-results:
		if result.err != nil {
			t.Fatalf("server-side handshake: %v", result.err)
		}
		if result.peerCommonName != "worker-7" {
			t.Fatalf("server verified the wrong peer identity: %q", result.peerCommonName)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server never completed its side of the handshake")
	}
}

// expectClientSideRejection proves a client is genuinely not authenticated,
// the way a real caller must: not by checking tls.Dial's error (TLS 1.3
// lets a client finish its own handshake step, and even successfully
// Write, before the server's rejection of a missing/untrusted certificate
// arrives - Dial succeeding is not evidence of anything), but by reading
// from the connection and observing the resulting alert. Any component
// built on this package must do the same: finishing Dial (or even one
// Write) is not proof of authentication.
func expectClientSideRejection(t *testing.T, conn *tls.Conn) {
	t.Helper()
	_, _ = conn.Write([]byte{1}) // may itself succeed; see the comment above
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("client was not rejected: a read after write succeeded")
	}
}

// TestMutualHandshakeRejectsAnUnauthenticatedClient proves the negative
// case: without MutualTLS, a well-behaved client presenting no
// certificate is not an authentication bypass - it is the well-known
// default that mutual auth exists specifically to close, so a component
// requiring authentication must always request MutualTLS explicitly.
func TestMutualHandshakeRejectsAnUnauthenticatedClient(t *testing.T) {
	ca := newTestCA(t)
	serverCert := issueLeaf(t, ca, "control-plane", []string{"control-plane.internal"})
	serverConfig, err := ServerConfig(Options{Certificates: []tls.Certificate{serverCert}, CAPEM: ca.caPEM, MutualTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	addr, results := serveOnce(t, serverConfig)

	clientConfig, err := ClientConfig(Options{CAPEM: ca.caPEM, ServerName: "control-plane.internal"})
	if err != nil {
		t.Fatal(err)
	}
	// Dial succeeding here is expected, not a bug: see
	// expectClientSideRejection's comment. Only Dial *failing* or a
	// subsequent Read failing are both valid evidence of rejection.
	if conn, err := tls.Dial("tcp", addr, clientConfig); err == nil {
		defer conn.Close()
		expectClientSideRejection(t, conn)
	}
	select {
	case result := <-results:
		if result.err == nil {
			t.Fatal("server accepted a client that presented no certificate")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server never observed the rejected handshake")
	}
}

// TestMutualHandshakeRejectsAnUntrustedClientCertificate proves a
// certificate valid in form but signed by the wrong CA is rejected, not
// merely one that is absent or malformed.
func TestMutualHandshakeRejectsAnUntrustedClientCertificate(t *testing.T) {
	ca := newTestCA(t)
	otherCA := newTestCA(t)
	serverCert := issueLeaf(t, ca, "control-plane", []string{"control-plane.internal"})
	untrustedClientCert := issueLeaf(t, otherCA, "impostor", nil)

	serverConfig, err := ServerConfig(Options{Certificates: []tls.Certificate{serverCert}, CAPEM: ca.caPEM, MutualTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	addr, results := serveOnce(t, serverConfig)

	clientConfig, err := ClientConfig(Options{
		Certificates: []tls.Certificate{untrustedClientCert}, CAPEM: ca.caPEM, ServerName: "control-plane.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := tls.Dial("tcp", addr, clientConfig)
	if err == nil {
		defer conn.Close()
		expectClientSideRejection(t, conn)
	}
	select {
	case result := <-results:
		if result.err == nil {
			t.Fatal("server accepted a client certificate signed by an untrusted CA")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server never observed the rejected handshake")
	}
}

// TestMutualHandshakeRejectsAnUntrustedServerCertificate proves the other
// direction: a client that expects the server to prove its identity
// against the shared CA refuses a server certificate signed by any other
// CA, even one otherwise well-formed for the correct name.
func TestMutualHandshakeRejectsAnUntrustedServerCertificate(t *testing.T) {
	ca := newTestCA(t)
	otherCA := newTestCA(t)
	impostorServerCert := issueLeaf(t, otherCA, "control-plane", []string{"control-plane.internal"})
	clientCert := issueLeaf(t, ca, "worker-7", nil)

	serverConfig, err := ServerConfig(Options{Certificates: []tls.Certificate{impostorServerCert}, CAPEM: ca.caPEM, MutualTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	addr, _ := serveOnce(t, serverConfig)

	clientConfig, err := ClientConfig(Options{
		Certificates: []tls.Certificate{clientCert}, CAPEM: ca.caPEM, ServerName: "control-plane.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tls.Dial("tcp", addr, clientConfig); err == nil {
		t.Fatal("client accepted a server certificate signed by an untrusted CA")
	}
}

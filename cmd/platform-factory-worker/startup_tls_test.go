package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the shipped startup path through a real authenticated registration.
// A deliberate 503 ends the process before the long-running polling loop.
func TestWorkerStartupRegistersAuthenticatedSignedIdentity(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ci-worker", Organization: []string{"worker"}}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	cert := filepath.Join(directory, "worker.pem")
	private := filepath.Join(directory, "key.pem")
	ca := filepath.Join(directory, "ca.pem")
	for name, data := range map[string][]byte{cert: certPEM, private: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})} {
		if err := os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	observed := make(chan Registration, 2)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/register" || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || r.TLS.PeerCertificates[0].Subject.CommonName != "ci-worker" {
			t.Error("registration lacks verified worker identity")
		}
		var registration Registration
		if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
			t.Error(err)
		}
		observed <- registration
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
	server.StartTLS()
	defer server.Close()
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"-control-plane=" + server.URL, "-cert=" + cert, "-key=" + private, "-ca=" + ca, "-sign-provenance", "-capabilities=cache,network", "-max-parallel=2"}
	for _, mode := range [][]string{{"-demo-simulate"}, {"-execution-root=" + filepath.Join(directory, "work")}} {
		err := run(append(append([]string{}, base...), mode...))
		if err == nil || !strings.Contains(err.Error(), "register:") {
			t.Fatalf("startup result: %v", err)
		}
		select {
		case registration := <-observed:
			if registration.Platform == "" || len(registration.PublicKey) == 0 || registration.MaxParallel != 2 || len(registration.Capabilities) != 2 {
				t.Fatalf("registration=%+v", registration)
			}
		default:
			t.Fatal("authenticated registration not observed")
		}
	}
}

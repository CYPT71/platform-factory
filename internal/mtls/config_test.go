package mtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func TestTLSConfigurations(t *testing.T) {
	cert, ca := testCertificate(t)
	tests := []struct {
		name    string
		options Options
		server  bool
		wantErr bool
	}{
		{"client defaults", Options{}, false, false},
		{"server needs certificate", Options{}, true, true},
		{"server TLS", Options{Certificates: []tls.Certificate{cert}}, true, false},
		{"mutual TLS needs CA", Options{Certificates: []tls.Certificate{cert}, MutualTLS: true}, true, true},
		{"mutual TLS", Options{Certificates: []tls.Certificate{cert}, CAPEM: ca, MutualTLS: true}, true, false},
		{"bad CA", Options{CAPEM: []byte("not PEM")}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var config *tls.Config
			var err error
			if tt.server {
				config, err = ServerConfig(tt.options)
			} else {
				config, err = ClientConfig(tt.options)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %t", err, tt.wantErr)
			}
			if err == nil && config.MinVersion != tls.VersionTLS12 {
				t.Fatalf("MinVersion = %d", config.MinVersion)
			}
			if tt.name == "mutual TLS" && config.ClientAuth != tls.RequireAndVerifyClientCert {
				t.Fatal("mTLS client verification is disabled")
			}
		})
	}
}

func testCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	return certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

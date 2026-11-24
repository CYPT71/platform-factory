package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CYPT71/platform-factory/internal/cache"
	"github.com/CYPT71/platform-factory/internal/control"
	"github.com/CYPT71/platform-factory/internal/mtls"
	"github.com/CYPT71/platform-factory/internal/quota"
)

type testPKI struct {
	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
	caPEM  []byte
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testPKI{
		caCert: cert,
		caKey:  caKey,
		caPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

func (p *testPKI) issueCert(t *testing.T, cn string, orgs, ous []string, server bool) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<60))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:         cn,
			Organization:       orgs,
			OrganizationalUnit: ous,
		},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if server {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		tmpl.DNSNames = []string{"localhost"}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.caCert, &key.PublicKey, p.caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	tlsCert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return tlsCert
}

func (p *testPKI) mtlsClient(t *testing.T, cert tls.Certificate) *http.Client {
	t.Helper()
	cfg, err := mtls.ClientConfig(mtls.Options{
		Certificates: []tls.Certificate{cert},
		CAPEM:        p.caPEM,
		ServerName:   "localhost",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
}

func TestMultiTenantMTLSNoCrossTenantLeakQuotaAndRestartRecovery(t *testing.T) {
	pki := newTestPKI(t)
	serverCert := pki.issueCert(t, "control-plane", []string{"control-plane"}, nil, true)
	tenantACert := pki.issueCert(t, "worker-alpha", []string{"worker"}, []string{"tenant:alpha"}, false)
	tenantBCert := pki.issueCert(t, "worker-beta", []string{"worker"}, []string{"tenant:beta"}, false)

	casRoot := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.json")
	tenantCAS, err := cache.OpenTenantManager(casRoot, 32) // 32-byte per-tenant quota
	if err != nil {
		t.Fatal(err)
	}

	serverTLS, err := mtls.ServerConfig(mtls.Options{
		Certificates: []tls.Certificate{serverCert},
		CAPEM:        pki.caPEM,
		MutualTLS:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	newServer := func(plane *control.ControlPlane) *httptest.Server {
		srv := &Server{
			Plane:     plane,
			StatePath: statePath,
			TenantCAS: tenantCAS,
			Scheduler: quota.NewFairScheduler(quota.NewTenantQuota(quota.Quota{MaxParallel: 2})),
		}
		if err := srv.RestoreQuotaUsage(); err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewUnstartedServer(srv.Routes())
		ts.TLS = serverTLS
		ts.StartTLS()
		return ts
	}

	plane1 := control.NewControlPlane(30 * time.Second)
	ts1 := newServer(plane1)
	defer ts1.Close()

	clientA := pki.mtlsClient(t, tenantACert)
	clientB := pki.mtlsClient(t, tenantBCert)

	// 1. Tenant A uploads a 20-byte blob into its isolated CAS.
	blobA := []byte("alpha-secret-payload")
	sumA := sha256.Sum256(blobA)
	digestA := "sha256:" + hex.EncodeToString(sumA[:])
	putReq, _ := http.NewRequest(http.MethodPut, ts1.URL+"/cas/blobs/"+digestA, bytes.NewReader(blobA))
	putReq.ContentLength = int64(len(blobA))
	resp, err := clientA.Do(putReq)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("tenant A PUT failed: resp=%v err=%v", resp, err)
	}
	resp.Body.Close()

	// Tenant A reads its own blob -> 200 OK.
	resp, err = clientA.Get(ts1.URL + "/cas/blobs/" + digestA)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("tenant A GET failed: resp=%v err=%v", resp, err)
	}
	gotA, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(gotA, blobA) {
		t.Fatalf("got=%q want=%q", gotA, blobA)
	}

	// Tenant B tries to read Tenant A's exact SHA-256 digest -> MUST be 404 Not Found (zero leakage)!
	resp, err = clientB.Get(ts1.URL + "/cas/blobs/" + digestA)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-tenant CAS read leaked: status=%d, want 404", resp.StatusCode)
	}

	// 2. Per-tenant disk quota (32 bytes): Tenant A cannot exceed 32 bytes (20 + 20 > 32 -> 507),
	//    while Tenant B still has its own fresh 32-byte quota!
	blobA2 := []byte("second-alpha-payload")
	sumA2 := sha256.Sum256(blobA2)
	digestA2 := "sha256:" + hex.EncodeToString(sumA2[:])
	putReq2, _ := http.NewRequest(http.MethodPut, ts1.URL+"/cas/blobs/"+digestA2, bytes.NewReader(blobA2))
	putReq2.ContentLength = int64(len(blobA2))
	resp, err = clientA.Do(putReq2)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("expected 507 Insufficient Storage for tenant A quota exceeded, got %d", resp.StatusCode)
	}

	putReqB, _ := http.NewRequest(http.MethodPut, ts1.URL+"/cas/blobs/"+digestA2, bytes.NewReader(blobA2))
	putReqB.ContentLength = int64(len(blobA2))
	resp, err = clientB.Do(putReqB)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("tenant B should succeed within its own quota: resp=%v err=%v", resp, err)
	}
	resp.Body.Close()

	// 3. Anti-spoofing: Tenant A cannot submit a lease claiming tenant "beta" -> 403 Forbidden.
	resp, err = clientA.Post(ts1.URL+"/lease/submit", "application/json", strings.NewReader(`{"payload":"spoof","required_platform":"linux/amd64","tenant":"beta"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for tenant spoofing, got %d", resp.StatusCode)
	}

	// Tenant A submits its own lease (omitting tenant -> stamped automatically from cert OU).
	resp, err = clientA.Post(ts1.URL+"/lease/submit", "application/json", strings.NewReader(`{"payload":"build-1","required_platform":"linux/amd64"}`))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("tenant A submit failed: resp=%v err=%v", resp, err)
	}
	var submitted struct {
		LeaseID string `json:"lease_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&submitted)
	resp.Body.Close()
	if submitted.LeaseID == "" {
		t.Fatal("missing lease_id")
	}

	// Tenant B cannot inspect Tenant A's lease -> 403 Forbidden.
	resp, err = clientB.Get(ts1.URL + "/lease/status?id=" + submitted.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-tenant lease status, got %d", resp.StatusCode)
	}

	// 4. Metrics endpoint over mTLS exposes live control-plane gauges.
	resp, err = clientA.Get(ts1.URL + "/metrics")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics failed: resp=%v err=%v", resp, err)
	}
	metricsBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(metricsBody), "platform_factory_leases_pending 1") {
		t.Fatalf("unexpected metrics body:\n%s", metricsBody)
	}

	// 5. Durable recovery across control-plane restart!
	ts1.Close()
	plane2, err := control.LoadControlPlane(30*time.Second, statePath)
	if err != nil {
		t.Fatalf("LoadControlPlane after restart: %v", err)
	}
	ts2 := newServer(plane2)
	defer ts2.Close()

	resp, err = clientA.Get(ts2.URL + "/lease/status?id=" + submitted.LeaseID)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("lease status after restart failed: resp=%v err=%v", resp, err)
	}
	var recovered control.Lease
	_ = json.NewDecoder(resp.Body).Decode(&recovered)
	resp.Body.Close()
	if recovered.Tenant != "alpha" || recovered.Payload != "build-1" || recovered.State != control.LeasePending {
		t.Fatalf("recovered lease mismatch: %+v", recovered)
	}
}

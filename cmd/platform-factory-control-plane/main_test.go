package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CYPT71/platform-factory/internal/control"
	"github.com/CYPT71/platform-factory/internal/observability"
)

func TestRunRejectsInvalidConfigurationBeforeListening(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"parse", []string{"-unknown"}, "flag provided but not defined"},
		{"credentials", nil, "all required"},
		{"listen", []string{"-cert=x", "-key=x", "-ca=x", "-listen="}, "must not be empty"},
		{"heartbeat", []string{"-cert=x", "-key=x", "-ca=x", "-heartbeat-timeout=0s"}, "greater than zero"},
		{"certificate", []string{"-cert=missing", "-key=missing", "-ca=missing"}, "load server certificate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := run(tt.args); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("run() err=%v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestRunInitializesDurableStateAndAuditBeforeListen(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeControlPlaneTestCertificate(t, dir)
	statePath := filepath.Join(dir, "state", "control.json")
	auditPath := filepath.Join(dir, "audit.jsonl")
	err := run([]string{
		"-cert=" + certPath, "-key=" + keyPath, "-ca=" + certPath,
		"-listen=invalid::address", "-state-file=" + statePath, "-audit-file=" + auditPath,
	})
	if err == nil {
		t.Fatal("invalid listen address unexpectedly served")
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("durable state was not initialized: %v", err)
	}
}

func TestRunRejectsInvalidDurableInputs(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeControlPlaneTestCertificate(t, dir)
	badCA := filepath.Join(dir, "bad-ca.pem")
	badState := filepath.Join(dir, "bad-state.json")
	badAudit := filepath.Join(dir, "bad-audit.jsonl")
	for path, content := range map[string]string{
		badCA: "not a certificate", badState: "not json", badAudit: "not json\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"missing ca", []string{"-cert=" + certPath, "-key=" + keyPath, "-ca=" + filepath.Join(dir, "missing")}, "read CA bundle"},
		{"invalid ca", []string{"-cert=" + certPath, "-key=" + keyPath, "-ca=" + badCA}, "build TLS config"},
		{"corrupt state", []string{"-cert=" + certPath, "-key=" + keyPath, "-ca=" + certPath, "-state-file=" + badState}, "restore scheduler state"},
		{"corrupt audit", []string{"-cert=" + certPath, "-key=" + keyPath, "-ca=" + certPath, "-audit-file=" + badAudit}, "open audit journal"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := run(test.args); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run() err=%v, want substring %q", err, test.want)
			}
		})
	}
}

func writeControlPlaneTestCertificate(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "control-plane"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestRunReapLoopStops(t *testing.T) {
	plane := control.NewControlPlane(time.Second)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		runReapLoop(plane, time.Millisecond, stop)
		close(done)
	}()
	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reap loop did not stop")
	}
}

func TestPersistentReapLoopSavesAndAuditsRequeuedLease(t *testing.T) {
	plane := control.NewControlPlane(time.Nanosecond)
	if err := plane.RegisterWorker("worker-a", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	id, err := plane.SubmitLease("work", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := plane.NextLease("worker-a"); err != nil || !ok {
		t.Fatalf("assign: ok=%v err=%v", ok, err)
	}
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	auditPath := filepath.Join(dir, "audit.jsonl")
	audit, err := control.OpenAuditJournal(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		runPersistentReapLoop(plane, time.Millisecond, statePath, audit, stop)
		close(done)
	}()
	deadline := time.After(time.Second)
	for {
		lease, _ := plane.LeaseStatus(id)
		if lease.State == control.LeasePending {
			break
		}
		select {
		case <-deadline:
			close(stop)
			t.Fatal("lease was not requeued")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(stop)
	<-done
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state not saved: %v", err)
	}
	events, err := control.VerifyAuditJournal(auditPath)
	if err != nil || len(events) != 1 || events[0].Action != "lease.requeued" || events[0].Subject != id {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

// syncBuffer makes bytes.Buffer safe for the concurrent writer (the reap
// loop's log calls) and reader (this test's polling loop) it is used with
// here; internal/observability's own mutex only protects logger state, not
// an io.Writer shared this way.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestPersistentReapLoopLogsStateSaveFailureViaObservability(t *testing.T) {
	buf := &syncBuffer{}
	observability.SetGlobalOutput(buf)
	t.Cleanup(func() { observability.SetGlobalOutput(os.Stderr) })

	plane := control.NewControlPlane(time.Nanosecond)
	if err := plane.RegisterWorker("worker-a", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err := plane.SubmitLease("work", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := plane.NextLease("worker-a"); err != nil || !ok {
		t.Fatalf("assign: ok=%v err=%v", ok, err)
	}

	// plane.Save calls os.MkdirAll on the parent directory, so a merely
	// missing directory would silently succeed. Block it deterministically
	// by making the parent path itself an existing regular file, which
	// MkdirAll cannot descend into regardless of the test's privileges.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	badStatePath := filepath.Join(blocker, "state.json")
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		runPersistentReapLoop(plane, time.Millisecond, badStatePath, nil, stop)
		close(done)
	}()
	deadline := time.After(time.Second)
	for {
		if strings.Contains(buf.String(), "failed to persist scheduler state after reap") {
			break
		}
		select {
		case <-deadline:
			close(stop)
			<-done
			t.Fatalf("expected a logged save failure, got: %s", buf.String())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(stop)
	<-done
	if !strings.Contains(buf.String(), badStatePath) {
		t.Fatalf("expected the failing path in the log output, got: %s", buf.String())
	}
}

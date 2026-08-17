package main

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CYPT71/platform-factory/internal/control"
)

// These tests exercise how the real HTTP handlers, not a simulation of
// them, behave under network fault conditions a hostile or merely
// unreliable network can actually produce: a captured request replayed
// later, the same request arriving more than once (duplication), and a
// corrupted body. "Partition" (the fourth classic fault) is exercised
// from the worker's side in cmd/platform-factory-worker/network_fault_test.go,
// since it's the client, not this server, whose behavior matters when the
// network disappears.

func submitAndAssign(t *testing.T, server *Server) (routes http.Handler, leaseID string) {
	t.Helper()
	routes = server.Routes()
	post := func(target, commonName string, body any) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, authenticatedRequest(http.MethodPost, target, commonName, body))
		return rec
	}
	if rec := post("/register", "worker-1", registerRequest{Platform: "linux/amd64"}); rec.Code != http.StatusOK {
		t.Fatalf("register: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := post("/lease/submit", "operator", submitRequest{Payload: "do the thing"})
	if rec.Code != http.StatusOK {
		t.Fatalf("submit: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var submitResp map[string]string
	decodeJSON(t, rec, &submitResp)
	leaseID = submitResp["lease_id"]
	if leaseID == "" {
		t.Fatal("no lease_id returned")
	}
	if rec := post("/lease/next", "worker-1", nil); rec.Code != http.StatusOK {
		t.Fatalf("next: status=%d body=%s", rec.Code, rec.Body.String())
	}
	return routes, leaseID
}

func leaseState(t *testing.T, routes http.Handler, leaseID string) control.Lease {
	t.Helper()
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, authenticatedRequest(http.MethodGet, "/lease/status?id="+leaseID, "worker-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("lease/status: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var lease control.Lease
	decodeJSON(t, rec, &lease)
	return lease
}

// TestHandleCompleteLeaseRejectsReplay proves a captured completion
// request, replayed verbatim after the original already succeeded, is
// rejected rather than silently accepted or double-applied - the
// classic replay attack on a non-idempotent-looking endpoint.
func TestHandleCompleteLeaseRejectsReplay(t *testing.T) {
	server := &Server{Plane: control.NewControlPlane(time.Minute)}
	routes, leaseID := submitAndAssign(t, server)

	firstBody := completeRequest{LeaseID: leaseID, Result: "done"}
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, authenticatedRequest(http.MethodPost, "/lease/complete", "worker-1", firstBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("first complete: status=%d body=%s", rec.Code, rec.Body.String())
	}
	afterFirst := leaseState(t, routes, leaseID)
	if afterFirst.State != "completed" || afterFirst.Result != "done" {
		t.Fatalf("unexpected state after first completion: %+v", afterFirst)
	}

	// Replay: the identical request, byte for byte in intent, arrives
	// again (an attacker resending a captured request, or a retried
	// client that never saw the first response).
	replay := httptest.NewRecorder()
	routes.ServeHTTP(replay, authenticatedRequest(http.MethodPost, "/lease/complete", "worker-1", firstBody))
	if replay.Code != http.StatusConflict {
		t.Fatalf("replayed complete: status=%d (want 409) body=%s", replay.Code, replay.Body.String())
	}

	afterReplay := leaseState(t, routes, leaseID)
	if afterReplay.CompletedAt != afterFirst.CompletedAt {
		t.Fatalf("replay mutated CompletedAt: before=%q after=%q", afterFirst.CompletedAt, afterReplay.CompletedAt)
	}
	if afterReplay.Result != "done" {
		t.Fatalf("replay changed the stored result: %+v", afterReplay)
	}
}

// TestHandleCompleteLeaseRejectsCorruptedBody proves a bit-flipped/
// truncated body - the shape of on-the-wire corruption, not an
// attacker crafting valid-but-wrong JSON - fails closed with 400 and
// leaves the lease untouched, rather than panicking or partially
// applying anything.
func TestHandleCompleteLeaseRejectsCorruptedBody(t *testing.T) {
	server := &Server{Plane: control.NewControlPlane(time.Minute)}
	routes, leaseID := submitAndAssign(t, server)

	valid, err := json.Marshal(completeRequest{LeaseID: leaseID, Result: "done"})
	if err != nil {
		t.Fatal(err)
	}
	corruptions := map[string]string{
		"truncated":        string(valid[:len(valid)/2]),
		"bit-flipped":      strings.Replace(string(valid), `"`, "\x00", 1),
		"trailing-garbage": string(valid) + "\x00\x01\x02not json",
		"empty":            "",
	}
	for name, corrupted := range corruptions {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/lease/complete", strings.NewReader(corrupted))
			certificate := &x509.Certificate{Subject: pkix.Name{CommonName: "worker-1", Organization: []string{workerRole}}}
			req.TLS = &tls.ConnectionState{
				PeerCertificates: []*x509.Certificate{certificate},
				VerifiedChains:   [][]*x509.Certificate{{certificate}},
			}
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: status=%d (want 400) body=%s", name, rec.Code, rec.Body.String())
			}
		})
	}

	// None of the corrupted attempts should have completed the lease.
	after := leaseState(t, routes, leaseID)
	if after.State != "assigned" {
		t.Fatalf("corrupted body affected lease state: %+v", after)
	}
}

// TestHandleCompleteLeaseHandlesConcurrentDuplicateRequests proves
// duplication - the same completion arriving twice at once, as a flaky
// network's retransmit could produce - resolves to exactly one success
// under -race, not a torn or double-applied state.
func TestHandleCompleteLeaseHandlesConcurrentDuplicateRequests(t *testing.T) {
	server := &Server{Plane: control.NewControlPlane(time.Minute)}
	routes, leaseID := submitAndAssign(t, server)
	body := completeRequest{LeaseID: leaseID, Result: "done"}

	const attempts = 8
	codes := make([]int, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, authenticatedRequest(http.MethodPost, "/lease/complete", "worker-1", body))
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()

	successes := 0
	for _, code := range codes {
		switch code {
		case http.StatusOK:
			successes++
		case http.StatusConflict:
			// expected for every loser of the race
		default:
			t.Fatalf("unexpected status %d among concurrent completions: %v", code, codes)
		}
	}
	if successes != 1 {
		t.Fatalf("want exactly 1 successful completion among %d concurrent duplicates, got %d: %v",
			attempts, successes, codes)
	}
}

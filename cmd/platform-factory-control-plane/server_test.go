package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CYPT71/platform-factory/internal/control"
	"github.com/CYPT71/platform-factory/internal/provenance"
	"github.com/CYPT71/platform-factory/internal/quota"
)

// authenticatedRequest builds a request as if it arrived over a real mTLS
// connection whose verified client certificate declares the worker role
// and has CommonName commonName - the same shape net/http populates
// r.TLS with on a real TLS listener, so the handlers under test exercise
// their real identity extraction path.
func authenticatedRequest(method, target, commonName string, body any) *http.Request {
	return requestWithCertificate(method, target, commonName, []string{workerRole}, body)
}

// requestWithCertificate is authenticatedRequest with an explicit
// Organization, so tests can prove a certificate that does not declare
// the worker role is rejected even with an otherwise valid identity.
func requestWithCertificate(method, target, commonName string, organization []string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, target, &buf)
	if commonName != "" {
		certificate := &x509.Certificate{Subject: pkix.Name{CommonName: commonName, Organization: organization}}
		req.TLS = &tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{certificate},
			VerifiedChains:   [][]*x509.Certificate{{certificate}},
		}
	}
	return req
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(rec.Body).Decode(target); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
}

func TestHandlersRejectRequestsWithoutAVerifiedCertificate(t *testing.T) {
	server := &Server{Plane: control.NewControlPlane(time.Minute)}
	for _, test := range []struct{ method, target string }{
		{http.MethodPost, "/register"},
		{http.MethodPost, "/heartbeat"},
		{http.MethodPost, "/lease/next"},
		{http.MethodPost, "/lease/complete"},
		{http.MethodPost, "/lease/cancel"},
		{http.MethodPost, "/lease/submit"},
		{http.MethodGet, "/lease/status?id=lease-1"},
		{http.MethodGet, "/workers"},
	} {
		req := authenticatedRequest(test.method, test.target, "", registerRequest{Platform: "linux/amd64"})
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status=%d body=%s", test.target, rec.Code, rec.Body.String())
		}
	}
}

func TestCancelLeaseOverHTTPIsDurableAndIdempotent(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	statePath := filepath.Join(t.TempDir(), "control.json")
	audit, err := control.OpenAuditJournal(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Plane: control.NewControlPlane(time.Minute), Audit: audit, StatePath: statePath}
	if err := server.Plane.RegisterWorker("worker-a", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	id, err := server.Plane.SubmitLease("work", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := server.Plane.NextLease("worker-a"); err != nil || !ok {
		t.Fatalf("assign: ok=%v err=%v", ok, err)
	}

	postCancel := func() (int, map[string]any) {
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, authenticatedRequest(http.MethodPost, "/lease/cancel", "operator-a", cancelRequest{LeaseID: id}))
		var response map[string]any
		decodeJSON(t, rec, &response)
		return rec.Code, response
	}
	if status, response := postCancel(); status != http.StatusOK || response["canceled"] != true {
		t.Fatalf("first cancel: status=%d response=%v", status, response)
	}
	if status, response := postCancel(); status != http.StatusOK || response["canceled"] != false {
		t.Fatalf("replayed cancel: status=%d response=%v", status, response)
	}

	restarted, err := control.LoadControlPlane(time.Minute, statePath)
	if err != nil {
		t.Fatal(err)
	}
	lease, ok := restarted.LeaseStatus(id)
	if !ok || lease.State != control.LeaseCanceled || lease.CanceledBy != "operator-a" || lease.CanceledAt.IsZero() {
		t.Fatalf("restarted lease=%+v ok=%v", lease, ok)
	}
	events, err := control.VerifyAuditJournal(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != "lease.canceled" || events[0].Subject != id || events[0].Actor != "operator-a" {
		t.Fatalf("audit events=%+v", events)
	}
}

// TestHandlersRejectCertificatesWithoutTheWorkerRole proves a certificate
// chaining to the trusted CA is not by itself enough: it must also
// declare the worker role, so a certificate this CA issued for some
// other purpose (a future operator or scheduler identity) cannot
// register or act as a worker merely by presenting a valid CommonName.
func TestHandlersRejectCertificatesWithoutTheWorkerRole(t *testing.T) {
	server := &Server{Plane: control.NewControlPlane(time.Minute)}
	for _, organization := range [][]string{nil, {"operator"}} {
		req := requestWithCertificate(http.MethodPost, "/register", "impostor", organization,
			registerRequest{Platform: "linux/amd64"})
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("organization=%v: status=%d body=%s", organization, rec.Code, rec.Body.String())
		}
	}
}

// TestRegisterIdentityComesFromTheCertificateNotTheBody proves a JSON
// body cannot claim to be a different worker: the request is
// authenticated as "worker-a" (the certificate CommonName), and even
// the registered identity is unambiguously "worker-a".
func TestRegisterIdentityComesFromTheCertificateNotTheBody(t *testing.T) {
	server := &Server{Plane: control.NewControlPlane(time.Minute)}
	req := authenticatedRequest(http.MethodPost, "/register", "worker-a", registerRequest{Platform: "linux/amd64"})
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	decodeJSON(t, rec, &resp)
	if resp["worker_id"] != "worker-a" {
		t.Fatalf("resp=%v", resp)
	}
	statuses := server.Plane.WorkerStatuses()
	if len(statuses) != 1 || statuses[0].ID != "worker-a" {
		t.Fatalf("statuses=%+v", statuses)
	}
}

// TestFullLifecycleOverHTTP drives register -> submit -> next -> complete
// through the actual HTTP handlers (not the control package directly),
// proving the wire format and status codes end to end.
func TestFullLifecycleOverHTTP(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	audit, err := control.OpenAuditJournal(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Plane: control.NewControlPlane(time.Minute), Audit: audit}
	routes := server.Routes()

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
	leaseID := submitResp["lease_id"]
	if leaseID == "" {
		t.Fatal("no lease_id returned")
	}

	rec = post("/lease/next", "worker-1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("next: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var lease control.Lease
	decodeJSON(t, rec, &lease)
	if lease.ID != leaseID || lease.Payload != "do the thing" {
		t.Fatalf("lease=%+v", lease)
	}

	// A second worker polling finds nothing left.
	if err := server.Plane.RegisterWorker("worker-2", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	rec = post("/lease/next", "worker-2", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("second next: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// worker-2 cannot complete worker-1's lease.
	rec = post("/lease/complete", "worker-2", completeRequest{LeaseID: leaseID, Result: "stolen"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("wrong-worker complete: status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = post("/lease/complete", "worker-1", completeRequest{LeaseID: leaseID, Result: "done"})
	if rec.Code != http.StatusOK {
		t.Fatalf("complete: status=%d body=%s", rec.Code, rec.Body.String())
	}

	statusRec := httptest.NewRecorder()
	routes.ServeHTTP(statusRec, authenticatedRequest(http.MethodGet, "/lease/status?id="+leaseID, "operator", nil))
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status: status=%d body=%s", statusRec.Code, statusRec.Body.String())
	}
	var final control.Lease
	decodeJSON(t, statusRec, &final)
	if final.State != control.LeaseCompleted || final.Result != "done" || final.Worker != "worker-1" ||
		final.CompletedBy != "worker-1" || final.CompletedAt.IsZero() {
		t.Fatalf("final=%+v", final)
	}
	events, err := control.VerifyAuditJournal(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	wantActions := []string{"worker.registered", "lease.submitted", "lease.assigned", "lease.completed"}
	if len(events) != len(wantActions) {
		t.Fatalf("audit events=%+v", events)
	}
	for index, action := range wantActions {
		if events[index].Action != action {
			t.Fatalf("audit event %d=%+v", index, events[index])
		}
	}
	if events[len(events)-1].Actor != "worker-1" || events[len(events)-1].Subject != leaseID {
		t.Fatalf("completion audit identity=%+v", events[len(events)-1])
	}
}

func TestCompletionIdentityCannotBeClaimedInRequestBody(t *testing.T) {
	server := &Server{Plane: control.NewControlPlane(time.Minute)}
	if err := server.Plane.RegisterWorker("worker-real", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	id, err := server.Plane.SubmitLease("work", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := server.Plane.NextLease("worker-real"); err != nil || !ok {
		t.Fatalf("assign: ok=%v err=%v", ok, err)
	}
	req := authenticatedRequest(http.MethodPost, "/lease/complete", "worker-real", map[string]any{
		"lease_id": id, "result": "done", "completed_by": "worker-forged",
	})
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	lease, _ := server.Plane.LeaseStatus(id)
	if lease.State != control.LeaseAssigned || lease.CompletedBy != "" {
		t.Fatalf("forged completion mutated lease: %+v", lease)
	}
}

func TestMutationHandlersFailClosedOnStateAndAuditErrors(t *testing.T) {
	newBrokenAudit := func(t *testing.T) *control.AuditJournal {
		t.Helper()
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		journal, err := control.OpenAuditJournal(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		return journal
	}
	post := func(server *Server, target, identity string, body any) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, authenticatedRequest(http.MethodPost, target, identity, body))
		return rec
	}

	t.Run("register validation", func(t *testing.T) {
		server := &Server{Plane: control.NewControlPlane(time.Minute)}
		rec := post(server, "/register", "worker-a", registerRequest{Platform: "invalid"})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("register persistence", func(t *testing.T) {
		server := &Server{Plane: control.NewControlPlane(time.Minute), StatePath: t.TempDir()}
		rec := post(server, "/register", "worker-a", registerRequest{Platform: "linux/amd64"})
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("register audit", func(t *testing.T) {
		server := &Server{Plane: control.NewControlPlane(time.Minute), Audit: newBrokenAudit(t)}
		rec := post(server, "/register", "worker-a", registerRequest{Platform: "linux/amd64"})
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("next unknown worker", func(t *testing.T) {
		server := &Server{Plane: control.NewControlPlane(time.Minute)}
		rec := post(server, "/lease/next", "worker-a", nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("submit validation", func(t *testing.T) {
		server := &Server{Plane: control.NewControlPlane(time.Minute)}
		rec := post(server, "/lease/submit", "operator", submitRequest{})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("submit audit", func(t *testing.T) {
		server := &Server{Plane: control.NewControlPlane(time.Minute), Audit: newBrokenAudit(t)}
		rec := post(server, "/lease/submit", "operator", submitRequest{Payload: "work"})
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestHTTPMutationPersistsLeaseForRestartRecovery(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "control", "state.json")
	server := &Server{
		Plane: control.NewControlPlane(time.Minute), StatePath: statePath,
	}
	routes := server.Routes()
	post := func(target, commonName string, body any) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, authenticatedRequest(http.MethodPost, target, commonName, body))
		return rec
	}
	if rec := post("/register", "worker-1", registerRequest{Platform: "linux/amd64"}); rec.Code != http.StatusOK {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	rec := post("/lease/submit", "operator", submitRequest{Payload: "survive restart"})
	if rec.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body.String())
	}
	var response map[string]string
	decodeJSON(t, rec, &response)
	if rec := post("/lease/next", "worker-1", nil); rec.Code != http.StatusOK {
		t.Fatalf("next: %d %s", rec.Code, rec.Body.String())
	}

	restarted, err := control.LoadControlPlane(time.Minute, statePath)
	if err != nil {
		t.Fatal(err)
	}
	lease, ok := restarted.LeaseStatus(response["lease_id"])
	if !ok || lease.State != control.LeasePending || lease.Worker != "" || lease.Attempt != 1 {
		t.Fatalf("restart did not reclaim lease: %+v ok=%v", lease, ok)
	}
}

func TestLeaseStatusReturnsNotFoundForUnknownLease(t *testing.T) {
	server := &Server{Plane: control.NewControlPlane(time.Minute)}
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, authenticatedRequest(http.MethodGet, "/lease/status?id=nope", "operator", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandlersRejectUnknownJSONFields proves a body trying to smuggle an
// unrecognized field - such as a claimed worker_id, which no request
// struct in this package defines - is rejected outright by
// DisallowUnknownFields, not silently ignored. Identity still comes only
// from the verified mTLS certificate (TestRegisterIdentityComesFromTheCertificateNotTheBody),
// and now a body that even tries otherwise never reaches that logic.
func TestHandlersRejectUnknownJSONFields(t *testing.T) {
	server := &Server{Plane: control.NewControlPlane(time.Minute)}
	req := authenticatedRequest(http.MethodPost, "/register", "worker-a", map[string]string{
		"platform": "linux/amd64", "worker_id": "worker-b",
	})
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if statuses := server.Plane.WorkerStatuses(); len(statuses) != 0 {
		t.Fatalf("a rejected request still registered a worker: %+v", statuses)
	}
}

func TestHandlersRejectTrailingJSON(t *testing.T) {
	server := &Server{Plane: control.NewControlPlane(time.Minute)}
	req := authenticatedRequest(http.MethodPost, "/register", "worker-a", nil)
	req.Body = io.NopCloser(strings.NewReader(`{"platform":"linux/amd64"} {}`))
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlersRejectMalformedTrailingData(t *testing.T) {
	server := &Server{Plane: control.NewControlPlane(time.Minute)}
	req := authenticatedRequest(http.MethodPost, "/register", "worker-a", nil)
	req.Body = io.NopCloser(strings.NewReader(`{"platform":"linux/amd64"} not-json`))
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestVerifiedWorkerIDRejectsEmptyCommonName(t *testing.T) {
	server := &Server{Plane: control.NewControlPlane(time.Minute)}
	req := requestWithCertificate(http.MethodPost, "/register", "temporary-name", []string{workerRole},
		registerRequest{Platform: "linux/amd64"})
	req.TLS.PeerCertificates[0].Subject.CommonName = ""
	req.TLS.VerifiedChains[0][0].Subject.CommonName = ""
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleHeartbeatSucceedsAndRejectsUnknownWorker(t *testing.T) {
	server := &Server{Plane: control.NewControlPlane(time.Minute)}
	routes := server.Routes()

	unknown := httptest.NewRecorder()
	routes.ServeHTTP(unknown, authenticatedRequest(http.MethodPost, "/heartbeat", "ghost", nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown worker heartbeat: status=%d body=%s", unknown.Code, unknown.Body.String())
	}

	register := httptest.NewRecorder()
	routes.ServeHTTP(register, authenticatedRequest(http.MethodPost, "/register", "worker-1", registerRequest{Platform: "linux/amd64"}))
	if register.Code != http.StatusOK {
		t.Fatalf("register: status=%d body=%s", register.Code, register.Body.String())
	}

	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, authenticatedRequest(http.MethodPost, "/heartbeat", "worker-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	decodeJSON(t, rec, &resp)
	if resp["worker_id"] != "worker-1" {
		t.Fatalf("resp=%v", resp)
	}
}

func TestHandleWorkersReturnsRegisteredWorkers(t *testing.T) {
	server := &Server{Plane: control.NewControlPlane(time.Minute)}
	routes := server.Routes()
	routes.ServeHTTP(httptest.NewRecorder(), authenticatedRequest(http.MethodPost, "/register", "worker-1", registerRequest{Platform: "linux/amd64"}))

	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, authenticatedRequest(http.MethodGet, "/workers", "operator", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var statuses []control.WorkerStatus
	decodeJSON(t, rec, &statuses)
	if len(statuses) != 1 || statuses[0].ID != "worker-1" {
		t.Fatalf("statuses=%+v", statuses)
	}
}

func TestHandleLeaseStatusRejectsMissingID(t *testing.T) {
	server := &Server{Plane: control.NewControlPlane(time.Minute)}
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, authenticatedRequest(http.MethodGet, "/lease/status", "operator", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestTenantQuotaRefusesSubmissionOverMaxParallelAndReleasesOnCompletion(t *testing.T) {
	scheduler := quota.NewFairScheduler(quota.NewTenantQuota(quota.Quota{MaxParallel: 1}))
	server := &Server{Plane: control.NewControlPlane(time.Minute), Scheduler: scheduler}
	routes := server.Routes()

	post := func(target string, body any) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, authenticatedRequest(http.MethodPost, target, "operator", body))
		return rec
	}

	// A submission with no declared tenant is never quota-checked, even
	// with a Scheduler configured and the tenant's single slot already
	// about to be exhausted below.
	if rec := post("/lease/submit", submitRequest{Payload: "untenanted"}); rec.Code != http.StatusOK {
		t.Fatalf("untenanted submit: status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec := post("/lease/submit", submitRequest{Payload: "first", Tenant: "acme"})
	if rec.Code != http.StatusOK {
		t.Fatalf("first tenant submit: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var first map[string]string
	decodeJSON(t, rec, &first)
	leaseID := first["lease_id"]
	if leaseID == "" {
		t.Fatal("no lease_id returned")
	}

	// The tenant's single MaxParallel slot is taken; a second concurrent
	// lease for the same tenant must be refused, not queued.
	rec = post("/lease/submit", submitRequest{Payload: "second", Tenant: "acme"})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second tenant submit: expected 429, got status=%d body=%s", rec.Code, rec.Body.String())
	}
	// A different tenant is unaffected by acme's exhausted quota.
	if rec := post("/lease/submit", submitRequest{Payload: "other tenant", Tenant: "widgets-inc"}); rec.Code != http.StatusOK {
		t.Fatalf("other tenant submit: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Assign and complete the first lease; that must release acme's slot.
	if err := serverCompleteLeaseAsWorker(t, server, "worker-1", leaseID); err != nil {
		t.Fatal(err)
	}

	if rec := post("/lease/submit", submitRequest{Payload: "third", Tenant: "acme"}); rec.Code != http.StatusOK {
		t.Fatalf("third tenant submit after release: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRestoreQuotaUsageRebuildsOpenTenantSlots(t *testing.T) {
	plane := control.NewControlPlane(time.Minute)
	leaseID, err := plane.SubmitLeaseWithTenant("work", "", nil, "", "acme", 0)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := quota.NewFairScheduler(quota.NewTenantQuota(quota.Quota{MaxParallel: 1}))
	server := &Server{Plane: plane, Scheduler: scheduler}
	if err := server.RestoreQuotaUsage(); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Schedule("acme", quota.ResourceTypeParallel, 1); err == nil {
		t.Fatal("restored open lease did not consume tenant quota")
	}
	server.releaseTenantSlot(leaseID)
	if err := scheduler.Schedule("acme", quota.ResourceTypeParallel, 1); err != nil {
		t.Fatalf("released restored slot remains charged: %v", err)
	}
}

// TestLeaseAssignmentPrefersHigherPriorityTenantOverHTTP proves the
// wiring end to end through the real HTTP handlers: a tenant's
// configured Quota.Priority, resolved by handleSubmitLease via
// Scheduler.GetPriority at submission time, actually reaches NextLease's
// placement decision - not just internal/placement's or internal/
// control's own isolated tests of the same property.
func TestLeaseAssignmentPrefersHigherPriorityTenantOverHTTP(t *testing.T) {
	tenants := quota.NewTenantQuota(quota.Quota{MaxParallel: 10})
	tenants.SetQuota("tenant-standard", quota.Quota{MaxParallel: 10, Priority: 0})
	tenants.SetQuota("tenant-premium", quota.Quota{MaxParallel: 10, Priority: 10})
	scheduler := quota.NewFairScheduler(tenants)
	server := &Server{Plane: control.NewControlPlane(time.Minute), Scheduler: scheduler}
	routes := server.Routes()

	post := func(target string, body any) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, authenticatedRequest(http.MethodPost, target, "operator", body))
		return rec
	}

	// Standard-priority tenant submits first.
	rec := post("/lease/submit", submitRequest{Payload: "standard work", RequiredPlatform: "linux/amd64", Tenant: "tenant-standard"})
	if rec.Code != http.StatusOK {
		t.Fatalf("standard submit: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var standardResp map[string]string
	decodeJSON(t, rec, &standardResp)

	// Premium-priority tenant submits second - later in FIFO order, but
	// higher priority.
	rec = post("/lease/submit", submitRequest{Payload: "premium work", RequiredPlatform: "linux/amd64", Tenant: "tenant-premium"})
	if rec.Code != http.StatusOK {
		t.Fatalf("premium submit: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var premiumResp map[string]string
	decodeJSON(t, rec, &premiumResp)

	regRec := httptest.NewRecorder()
	routes.ServeHTTP(regRec, authenticatedRequest(http.MethodPost, "/register", "worker-1", registerRequest{Platform: "linux/amd64", MaxParallel: 10}))
	if regRec.Code != http.StatusOK {
		t.Fatalf("register: status=%d body=%s", regRec.Code, regRec.Body.String())
	}

	nextRec := httptest.NewRecorder()
	routes.ServeHTTP(nextRec, authenticatedRequest(http.MethodPost, "/lease/next", "worker-1", nil))
	if nextRec.Code != http.StatusOK {
		t.Fatalf("first next: status=%d body=%s", nextRec.Code, nextRec.Body.String())
	}
	var first control.Lease
	decodeJSON(t, nextRec, &first)
	if first.ID != premiumResp["lease_id"] {
		t.Fatalf("want the premium (later-submitted, higher-priority) lease first, got %+v (standard=%s premium=%s)",
			first, standardResp["lease_id"], premiumResp["lease_id"])
	}
	if first.Priority != 10 {
		t.Fatalf("assigned lease lost its resolved priority: %+v", first)
	}

	nextRec = httptest.NewRecorder()
	routes.ServeHTTP(nextRec, authenticatedRequest(http.MethodPost, "/lease/next", "worker-1", nil))
	if nextRec.Code != http.StatusOK {
		t.Fatalf("second next: status=%d body=%s", nextRec.Code, nextRec.Body.String())
	}
	var second control.Lease
	decodeJSON(t, nextRec, &second)
	if second.ID != standardResp["lease_id"] {
		t.Fatalf("want the standard lease second, got %+v", second)
	}
}

// serverCompleteLeaseAsWorker assigns leaseID to a freshly registered
// worker and completes it, exercising the real HTTP handlers so the
// quota-release wiring in handleCompleteLease is what's under test, not a
// bypass of it.
func serverCompleteLeaseAsWorker(t *testing.T, server *Server, workerID, leaseID string) error {
	t.Helper()
	routes := server.Routes()
	post := func(target, commonName string, body any) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, authenticatedRequest(http.MethodPost, target, commonName, body))
		return rec
	}
	// MaxParallel is generous, not 1: the loop below may need to pull
	// several unrelated pending leases before it reaches leaseID, and a
	// worker at capacity cannot be assigned more work until it completes
	// what it's already holding.
	if rec := post("/register", workerID, registerRequest{Platform: "linux/amd64", MaxParallel: 100}); rec.Code != http.StatusOK {
		return fmt.Errorf("register: status=%d body=%s", rec.Code, rec.Body.String())
	}
	for {
		rec := post("/lease/next", workerID, nil)
		if rec.Code != http.StatusOK {
			return fmt.Errorf("next: status=%d body=%s", rec.Code, rec.Body.String())
		}
		var lease control.Lease
		decodeJSON(t, rec, &lease)
		if lease.ID == leaseID {
			break
		}
		if lease.ID == "" {
			return fmt.Errorf("lease %s was never assigned to %s", leaseID, workerID)
		}
	}
	if rec := post("/lease/complete", workerID, completeRequest{LeaseID: leaseID, Result: "ok"}); rec.Code != http.StatusOK {
		return fmt.Errorf("complete: status=%d body=%s", rec.Code, rec.Body.String())
	}
	return nil
}

func TestVerifiedWorkerCompletionRequiresAndChecksProvenanceSignature(t *testing.T) {
	identity, privateKey, err := provenance.GenerateWorkloadIdentity("worker-signed")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := provenance.NewWorkloadSigner(identity, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	otherIdentity, otherPrivateKey, err := provenance.GenerateWorkloadIdentity("attacker")
	if err != nil {
		t.Fatal(err)
	}
	otherSigner, err := provenance.NewWorkloadSigner(otherIdentity, otherPrivateKey)
	if err != nil {
		t.Fatal(err)
	}

	newServer := func() (*Server, func(target, commonName string, body any) *httptest.ResponseRecorder) {
		server := &Server{Plane: control.NewControlPlane(time.Minute), Provenance: provenance.NewProvenanceStore()}
		routes := server.Routes()
		return server, func(target, commonName string, body any) *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, authenticatedRequest(http.MethodPost, target, commonName, body))
			return rec
		}
	}
	registerAndAssign := func(server *Server, post func(string, string, any) *httptest.ResponseRecorder) string {
		t.Helper()
		if rec := post("/register", "worker-signed", registerRequest{Platform: "linux/amd64", PublicKey: identity.PublicKey}); rec.Code != http.StatusOK {
			t.Fatalf("register: status=%d body=%s", rec.Code, rec.Body.String())
		}
		rec := post("/lease/submit", "operator", submitRequest{Payload: "work"})
		var resp map[string]string
		decodeJSON(t, rec, &resp)
		leaseID := resp["lease_id"]
		if rec := post("/lease/next", "worker-signed", nil); rec.Code != http.StatusOK {
			t.Fatalf("next: status=%d body=%s", rec.Code, rec.Body.String())
		}
		return leaseID
	}

	t.Run("unsigned completion is refused once a public key is registered", func(t *testing.T) {
		server, post := newServer()
		leaseID := registerAndAssign(server, post)
		rec := post("/lease/complete", "worker-signed", completeRequest{LeaseID: leaseID, Result: "ok"})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("a signature from a different identity is refused", func(t *testing.T) {
		server, post := newServer()
		leaseID := registerAndAssign(server, post)
		record := &provenance.ProvenanceRecord{BuildID: leaseID, WorkerID: "worker-signed"}
		if err := otherSigner.Sign(record); err != nil {
			t.Fatal(err)
		}
		rec := post("/lease/complete", "worker-signed", completeRequest{LeaseID: leaseID, Result: "ok", Provenance: record})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("a record for a different lease is refused, not silently accepted", func(t *testing.T) {
		server, post := newServer()
		leaseID := registerAndAssign(server, post)
		record := &provenance.ProvenanceRecord{BuildID: "some-other-lease", WorkerID: "worker-signed"}
		if err := signer.Sign(record); err != nil {
			t.Fatal(err)
		}
		rec := post("/lease/complete", "worker-signed", completeRequest{LeaseID: leaseID, Result: "ok", Provenance: record})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("a correctly signed record is accepted and stored", func(t *testing.T) {
		server, post := newServer()
		leaseID := registerAndAssign(server, post)
		record := &provenance.ProvenanceRecord{BuildID: leaseID, WorkerID: "worker-signed"}
		if err := signer.Sign(record); err != nil {
			t.Fatal(err)
		}
		rec := post("/lease/complete", "worker-signed", completeRequest{LeaseID: leaseID, Result: "ok", Provenance: record})
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		stored, ok := server.Provenance.Get(leaseID)
		if !ok || stored.SignedBy != identity.ID {
			t.Fatalf("stored=%+v ok=%v", stored, ok)
		}
	})

	t.Run("a worker that never registered a public key is unaffected", func(t *testing.T) {
		_, post := newServer()
		if rec := post("/register", "worker-plain", registerRequest{Platform: "linux/amd64"}); rec.Code != http.StatusOK {
			t.Fatalf("register: status=%d body=%s", rec.Code, rec.Body.String())
		}
		rec := post("/lease/submit", "operator", submitRequest{Payload: "work"})
		var resp map[string]string
		decodeJSON(t, rec, &resp)
		leaseID := resp["lease_id"]
		if rec := post("/lease/next", "worker-plain", nil); rec.Code != http.StatusOK {
			t.Fatalf("next: status=%d body=%s", rec.Code, rec.Body.String())
		}
		if rec := post("/lease/complete", "worker-plain", completeRequest{LeaseID: leaseID, Result: "ok"}); rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
}

// platform-factory-control-plane exposes internal/control's coordination logic
// over an authenticated HTTP API. Every mutating call's caller identity is
// taken from the verified mTLS client certificate's CommonName - never
// from the request body - so a worker cannot act as, or complete work on
// behalf of, another worker merely by naming it in JSON.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/CYPT71/secure-oci-base/internal/control"
	"github.com/CYPT71/secure-oci-base/internal/mtls"
	"github.com/CYPT71/secure-oci-base/internal/observability"
	"github.com/CYPT71/secure-oci-base/internal/provenance"
	"github.com/CYPT71/secure-oci-base/internal/quota"
)

const maxRequestBodyBytes = 1 << 20

// workerRole is the Subject Organization value every worker-facing
// certificate must declare, distinct from CommonName (each worker's own
// unique identity). See internal/mtls.HasRole.
const workerRole = "worker"

// Server adapts a *control.ControlPlane to HTTP.
type Server struct {
	Plane     *control.ControlPlane
	StatePath string
	Audit     *control.AuditJournal

	// Provenance, if non-nil, stores verified signed completion records
	// (see verifyCompletionProvenance). Nil disables both storage and, by
	// extension, is compatible with any worker that never registers a
	// PublicKey - verification only ever runs for a worker that opted in.
	Provenance *provenance.ProvenanceStore

	// Scheduler, if non-nil, enforces a per-tenant concurrent-lease quota
	// (ResourceTypeParallel) on submissions that declare a tenant. Leases
	// that omit Tenant are never quota-checked, preserving prior behavior
	// for callers that don't opt in. Nil disables quota enforcement
	// entirely, which is the zero-value default every existing caller and
	// test gets.
	Scheduler *quota.FairScheduler

	// leaseTenant remembers which tenant a still-open lease was submitted
	// for, so its concurrency slot can be released on completion or
	// cancellation. This is intentionally in-memory only, not persisted
	// in control.Lease or the durable snapshot: a control-plane restart
	// resets in-flight quota accounting along with it, which is an
	// accepted limitation of this first integration, not a durability
	// promise.
	tenantMu    sync.Mutex
	leaseTenant map[string]quota.TenantID
}

// reserveTenantSlot checks and reserves one concurrent-lease slot for
// tenant, if a Scheduler is configured and a tenant was declared. It
// returns a non-nil error (already a *quota.QuotaExceededError when the
// quota was the reason) if the reservation was refused.
func (s *Server) reserveTenantSlot(tenant string) error {
	if s.Scheduler == nil || tenant == "" {
		return nil
	}
	return s.Scheduler.Schedule(quota.TenantID(tenant), quota.ResourceTypeParallel, 1)
}

// releaseTenantSlot releases the concurrency slot reserved for leaseID, if
// any was reserved.
func (s *Server) releaseTenantSlot(leaseID string) {
	if s.Scheduler == nil {
		return
	}
	s.tenantMu.Lock()
	tenant, ok := s.leaseTenant[leaseID]
	if ok {
		delete(s.leaseTenant, leaseID)
	}
	s.tenantMu.Unlock()
	if ok {
		s.Scheduler.Release(tenant, quota.ResourceTypeParallel, 1)
	}
}

// verifyCompletionProvenance enforces that a worker which registered a
// PublicKey signs every lease it completes, and that the signature
// actually verifies against that key for this specific worker and lease -
// closing the gap a worker could otherwise exploit by simply omitting
// Provenance, or by replaying a validly-signed record from a different
// lease or a different worker's completion.
//
// A worker that never registered a PublicKey is unaffected either way:
// this always succeeds for it, whether or not it sends a record, since
// there is nothing to verify the signature against.
func (s *Server) verifyCompletionProvenance(workerID, leaseID string, record *provenance.ProvenanceRecord) error {
	status, ok := s.Plane.WorkerStatus(workerID)
	if !ok || status.PublicKey == "" {
		return nil
	}
	if record == nil {
		return fmt.Errorf("worker %q registered a public key and must sign every completion", workerID)
	}
	if record.WorkerID != workerID {
		return fmt.Errorf("provenance worker_id %q does not match the completing worker %q", record.WorkerID, workerID)
	}
	if record.BuildID != leaseID {
		return fmt.Errorf("provenance build_id %q does not match lease %q", record.BuildID, leaseID)
	}
	if err := provenance.Verify(record, status.PublicKey); err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}
	return nil
}

// releaseSlotWithoutLease undoes a reserveTenantSlot reservation for a
// submission that was refused before a lease ID existed to track it
// against.
func (s *Server) releaseSlotWithoutLease(tenant string) {
	if s.Scheduler == nil || tenant == "" {
		return
	}
	s.Scheduler.Release(quota.TenantID(tenant), quota.ResourceTypeParallel, 1)
}

// rememberTenantSlot records which tenant successfully reserved a slot for
// leaseID, so it can be released later.
func (s *Server) rememberTenantSlot(leaseID, tenant string) {
	if s.Scheduler == nil || tenant == "" {
		return
	}
	s.tenantMu.Lock()
	if s.leaseTenant == nil {
		s.leaseTenant = make(map[string]quota.TenantID)
	}
	s.leaseTenant[leaseID] = quota.TenantID(tenant)
	s.tenantMu.Unlock()
}

func (s *Server) persist(w http.ResponseWriter) bool {
	if s.StatePath == "" {
		return true
	}
	if err := s.Plane.Save(s.StatePath); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("persist scheduler state: %w", err))
		return false
	}
	return true
}

func (s *Server) audit(w http.ResponseWriter, actor, action, subject string) bool {
	if s.Audit == nil {
		return true
	}
	if _, err := s.Audit.Append(actor, action, subject); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("append audit event: %w", err))
		return false
	}
	return true
}

func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /register", s.handleRegister)
	mux.HandleFunc("POST /heartbeat", s.handleHeartbeat)
	mux.HandleFunc("POST /lease/next", s.handleNextLease)
	mux.HandleFunc("POST /lease/complete", s.handleCompleteLease)
	mux.HandleFunc("POST /lease/cancel", s.handleCancelLease)
	mux.HandleFunc("POST /lease/submit", s.handleSubmitLease)
	mux.HandleFunc("GET /lease/status", s.handleLeaseStatus)
	mux.HandleFunc("GET /workers", s.handleWorkers)
	return mux
}

// verifiedWorkerID returns the CommonName of the request's verified mTLS
// client certificate, after also requiring the certificate declare the
// worker role in its Subject Organization: CommonName alone is each
// worker's own unique identity, not proof the certificate was ever meant
// to authenticate as a worker at all. It fails closed: no certificate,
// no declared role, no identity.
func verifiedWorkerID(r *http.Request) (string, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || len(r.TLS.VerifiedChains) == 0 {
		return "", errors.New("no verified client certificate on this connection")
	}
	leaf := r.TLS.VerifiedChains[0][0]
	if leaf == nil {
		return "", errors.New("verified client certificate chain has no leaf")
	}
	if !mtls.HasRole(leaf, workerRole) {
		return "", fmt.Errorf("client certificate does not declare the %q role", workerRole)
	}
	name := leaf.Subject.CommonName
	if name == "" {
		return "", errors.New("client certificate has no CommonName")
	}
	return name, nil
}

func decodeRequest(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return fmt.Errorf("trailing request data: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// statusForError maps a control-plane error to an HTTP status. The
// idempotency guard (a lease no longer assigned to the caller) is a 409
// Conflict, not a 500: it is an expected, correctly-handled outcome of
// worker loss and reassignment, not a server fault.
func statusForError(err error) int {
	switch {
	case errors.Is(err, control.ErrUnknownWorker), errors.Is(err, control.ErrUnknownLease):
		return http.StatusNotFound
	case errors.Is(err, control.ErrLeaseNotAssignedToWorker), errors.Is(err, control.ErrLeaseAlreadyCompleted):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

type registerRequest struct {
	Platform      string   `json:"platform"`
	Capabilities  []string `json:"capabilities,omitempty"`
	CachedContent []string `json:"cached_content,omitempty"`
	MaxParallel   int      `json:"max_parallel,omitempty"`
	// PublicKey, if set, is this worker's base64-encoded Ed25519 public
	// key. When present, handleCompleteLease requires and verifies a
	// signed provenance record against it for every lease this worker
	// completes; omitting it (the default) keeps completion exactly as
	// before.
	PublicKey string `json:"public_key,omitempty"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	workerID, err := verifiedWorkerID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var req registerRequest
	if err := decodeRequest(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.MaxParallel == 0 {
		req.MaxParallel = 1
	}
	if err := s.Plane.RegisterWorkerWithOptions(workerID, control.WorkerRegistration{
		Platform: req.Platform, Capabilities: req.Capabilities,
		CachedContent: req.CachedContent, MaxParallel: req.MaxParallel, PublicKey: req.PublicKey,
	}); err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	if !s.persist(w) {
		return
	}
	if !s.audit(w, workerID, "worker.registered", workerID) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"worker_id": workerID})
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	workerID, err := verifiedWorkerID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if err := s.Plane.Heartbeat(workerID); err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	if !s.persist(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"worker_id": workerID})
}

func (s *Server) handleNextLease(w http.ResponseWriter, r *http.Request) {
	workerID, err := verifiedWorkerID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	lease, ok, err := s.Plane.NextLease(workerID)
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !s.persist(w) {
		return
	}
	if !s.audit(w, workerID, "lease.assigned", lease.ID) {
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

type completeRequest struct {
	LeaseID string `json:"lease_id"`
	Result  string `json:"result"`
	// Provenance, if set, must be a provenance.ProvenanceRecord signed by
	// the private key matching this worker's registered PublicKey. A
	// worker that registered a PublicKey but omits Provenance, or whose
	// signature does not verify, has its completion refused - once a
	// worker opts into signing, every completion must be signed.
	Provenance *provenance.ProvenanceRecord `json:"provenance,omitempty"`
}

func (s *Server) handleCompleteLease(w http.ResponseWriter, r *http.Request) {
	workerID, err := verifiedWorkerID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var req completeRequest
	if err := decodeRequest(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.verifyCompletionProvenance(workerID, req.LeaseID, req.Provenance); err != nil {
		observability.Warn("lease completion refused: provenance verification failed",
			observability.Fields{"worker": workerID, "lease_id": req.LeaseID, "error": err.Error()})
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if err := s.Plane.CompleteLease(workerID, req.LeaseID, req.Result); err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	if req.Provenance != nil && s.Provenance != nil {
		if err := s.Provenance.Store(req.Provenance); err != nil {
			// The lease is already committed complete at this point;
			// failing to cache its provenance record is not reason to
			// fail the whole request, only to note it.
			observability.Warn("failed to store verified provenance record",
				observability.Fields{"worker": workerID, "lease_id": req.LeaseID, "error": err.Error()})
		}
	}
	s.releaseTenantSlot(req.LeaseID)
	if !s.persist(w) {
		return
	}
	if !s.audit(w, workerID, "lease.completed", req.LeaseID) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"lease_id": req.LeaseID})
}

type cancelRequest struct {
	LeaseID string `json:"lease_id"`
}

func (s *Server) handleCancelLease(w http.ResponseWriter, r *http.Request) {
	actor, err := verifiedWorkerID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var req cancelRequest
	if err := decodeRequest(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	changed, err := s.Plane.CancelLease(actor, req.LeaseID)
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	if changed {
		s.releaseTenantSlot(req.LeaseID)
	}
	if !s.persist(w) {
		return
	}
	if changed && !s.audit(w, actor, "lease.canceled", req.LeaseID) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lease_id": req.LeaseID, "canceled": changed})
}

type submitRequest struct {
	Payload              string   `json:"payload"`
	RequiredPlatform     string   `json:"required_platform"`
	RequiredCapabilities []string `json:"required_capabilities,omitempty"`
	PreferredContent     string   `json:"preferred_content,omitempty"`
	// Tenant, if set and a Scheduler is configured, is checked and charged
	// one concurrent-lease slot for the lease's lifetime. Omitting it
	// opts this submission out of quota enforcement entirely.
	Tenant string `json:"tenant,omitempty"`
}

// handleSubmitLease is called by an operator/CLI, not a worker - it does
// not require a worker identity, only a valid mTLS client certificate
// (enforced by the server's own MutualTLS requirement, checked before any
// handler runs).
func (s *Server) handleSubmitLease(w http.ResponseWriter, r *http.Request) {
	actor, err := verifiedWorkerID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var req submitRequest
	if err := decodeRequest(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.reserveTenantSlot(req.Tenant); err != nil {
		observability.Warn("lease submission refused by tenant quota", observability.Fields{"tenant": req.Tenant, "error": err.Error()})
		writeError(w, http.StatusTooManyRequests, err)
		return
	}
	priority := 0
	if req.Tenant != "" && s.Scheduler != nil {
		// A snapshot at submission time, not re-resolved later - see
		// control.Lease.Priority's doc comment for why that's the
		// intended design, not a shortcut.
		priority = s.Scheduler.GetPriority(quota.TenantID(req.Tenant))
	}
	id, err := s.Plane.SubmitLeaseWithTenant(req.Payload, req.RequiredPlatform,
		req.RequiredCapabilities, req.PreferredContent, req.Tenant, priority)
	if err != nil {
		s.releaseSlotWithoutLease(req.Tenant)
		writeError(w, statusForError(err), err)
		return
	}
	s.rememberTenantSlot(id, req.Tenant)
	if !s.persist(w) {
		return
	}
	if !s.audit(w, actor, "lease.submitted", id) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"lease_id": id})
}

func (s *Server) handleLeaseStatus(w http.ResponseWriter, r *http.Request) {
	if _, err := verifiedWorkerID(r); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, errors.New("lease id is required"))
		return
	}
	lease, ok := s.Plane.LeaseStatus(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("%w: %q", control.ErrUnknownLease, id))
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

func (s *Server) handleWorkers(w http.ResponseWriter, r *http.Request) {
	if _, err := verifiedWorkerID(r); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	writeJSON(w, http.StatusOK, s.Plane.WorkerStatuses())
}

// runReapLoop periodically requeues leases held by workers whose
// heartbeat has expired, until ctx is done.
func runReapLoop(plane *control.ControlPlane, interval time.Duration, stop <-chan struct{}) {
	runPersistentReapLoop(plane, interval, "", nil, stop)
}

func runPersistentReapLoop(plane *control.ControlPlane, interval time.Duration, statePath string, audit *control.AuditJournal, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			requeued := plane.Reap(time.Now())
			if len(requeued) > 0 {
				observability.Info("reaped expired leases", observability.Fields{"count": len(requeued)})
			}
			if statePath != "" {
				if err := plane.Save(statePath); err != nil {
					observability.Warn("failed to persist scheduler state after reap", observability.Fields{"path": statePath, "error": err.Error()})
				}
			}
			if audit != nil {
				for _, leaseID := range requeued {
					if _, err := audit.Append("control-plane", "lease.requeued", leaseID); err != nil {
						observability.Warn("failed to append audit entry", observability.Fields{"lease_id": leaseID, "error": err.Error()})
					}
				}
			}
		case <-stop:
			return
		}
	}
}

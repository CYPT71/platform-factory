// Package control coordinates workers and leases independently of transport.
package control

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	typederrors "github.com/CYPT71/platform-factory/internal/errors"
	"github.com/CYPT71/platform-factory/internal/placement"
	"github.com/CYPT71/platform-factory/internal/provenance"
)

// WorkerStatus is a snapshot of a registered worker.
type WorkerStatus struct {
	ID            string    `json:"id"`
	Platform      string    `json:"platform"`
	Capabilities  []string  `json:"capabilities,omitempty"`
	CachedContent []string  `json:"cached_content,omitempty"`
	MaxParallel   int       `json:"max_parallel"`
	RegisteredAt  time.Time `json:"registered_at"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	// PublicKey verifies completion provenance but does not establish identity.
	PublicKey string `json:"public_key,omitempty"`
}

// WorkerRegistration describes scheduling facts asserted by a worker.
type WorkerRegistration struct {
	Platform      string
	Capabilities  []string
	CachedContent []string
	MaxParallel   int
	// PublicKey is an optional base64-encoded Ed25519 public key.
	PublicKey string
}

// LeaseState is a lease's position in its lifecycle.
type LeaseState string

const (
	LeasePending   LeaseState = "pending"
	LeaseAssigned  LeaseState = "assigned"
	LeaseCompleted LeaseState = "completed"
	LeaseCanceled  LeaseState = "canceled"
)

// Lease is one unit of distributable work. Payload and Result are opaque
// to this package - callers define what they mean.
type Lease struct {
	ID                   string   `json:"id"`
	Payload              string   `json:"payload"`
	RequiredPlatform     string   `json:"required_platform,omitempty"` // empty matches any worker
	RequiredCapabilities []string `json:"required_capabilities,omitempty"`
	PreferredContent     string   `json:"preferred_content,omitempty"`
	// Tenant is opaque quota ownership metadata.
	Tenant string `json:"tenant,omitempty"`
	// Priority is a submission-time placement priority; higher values win.
	Priority    int        `json:"priority,omitempty"`
	State       LeaseState `json:"state"`
	Worker      string     `json:"worker,omitempty"`
	Attempt     int        `json:"attempt"`
	Result      string     `json:"result,omitempty"`
	AssignedAt  time.Time  `json:"assigned_at,omitempty"`
	CompletedBy string     `json:"completed_by,omitempty"`
	CompletedAt time.Time  `json:"completed_at,omitempty"`
	CanceledBy  string     `json:"canceled_by,omitempty"`
	CanceledAt  time.Time  `json:"canceled_at,omitempty"`
}

const maxOpaqueFieldBytes = 1 << 20

var (
	workerIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	platformPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*/[a-z0-9][a-z0-9_.-]{0,63}$`)
	capabilityPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:/-]{0,127}$`)
	contentPattern    = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

var (
	// ErrUnknownWorker identifies an unregistered worker.
	ErrUnknownWorker = errors.New("control: unknown worker")
	// ErrUnknownLease identifies a missing lease.
	ErrUnknownLease = errors.New("control: unknown lease")
	// ErrLeaseNotAssignedToWorker rejects stale completion attempts.
	ErrLeaseNotAssignedToWorker = errors.New("control: lease is not currently assigned to this worker")
	// ErrLeaseAlreadyCompleted rejects cancellation after completion.
	ErrLeaseAlreadyCompleted = errors.New("control: completed lease cannot be canceled")
)

// ControlPlane holds all coordination state in memory, guarded by a single
// mutex; every exported method is safe for concurrent use.
type ControlPlane struct {
	heartbeatTimeout time.Duration
	now              func() time.Time

	mu      sync.Mutex
	workers map[string]*WorkerStatus
	leases  map[string]*Lease
	pending []string // lease IDs, FIFO
	nextID  int
}

// NewControlPlane returns a ControlPlane that considers a worker lost once
// heartbeatTimeout has elapsed since its last heartbeat.
func NewControlPlane(heartbeatTimeout time.Duration) *ControlPlane {
	return &ControlPlane{
		heartbeatTimeout: heartbeatTimeout,
		now:              time.Now,
		workers:          map[string]*WorkerStatus{},
		leases:           map[string]*Lease{},
	}
}

// RegisterWorker records a worker with legacy defaults. Re-registration means
// a new process instance and requeues leases held by the previous instance.
func (c *ControlPlane) RegisterWorker(id, platform string) error {
	return c.RegisterWorkerWithOptions(id, WorkerRegistration{Platform: platform, MaxParallel: 1024})
}

// RegisterWorkerWithOptions records scheduling capabilities and cache
// locality without trusting unbounded or ambiguous worker metadata.
func (c *ControlPlane) RegisterWorkerWithOptions(id string, registration WorkerRegistration) error {
	if !workerIDPattern.MatchString(id) {
		return typederrors.New(typederrors.CodeInvalidArgument, "control: worker id must be 1-128 ASCII letters, digits, dots, underscores, or hyphens")
	}
	if !platformPattern.MatchString(registration.Platform) {
		return typederrors.New(typederrors.CodeInvalidArgument, "control: platform must have the form os/architecture")
	}
	capabilities, err := normalizedTokens(registration.Capabilities, capabilityPattern, 64, "capability")
	if err != nil {
		return err
	}
	cachedContent, err := normalizedTokens(registration.CachedContent, contentPattern, 4096, "cached content digest")
	if err != nil {
		return err
	}
	if registration.MaxParallel <= 0 || registration.MaxParallel > 1024 {
		return typederrors.New(typederrors.CodeInvalidArgument, "control: max parallel must be between 1 and 1024")
	}
	if registration.PublicKey != "" {
		key, err := base64.StdEncoding.DecodeString(registration.PublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return typederrors.New(typederrors.CodeInvalidArgument, "control: public key must be a base64-encoded 32-byte Ed25519 key")
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if worker, ok := c.workers[id]; ok {
		worker.Platform = registration.Platform
		worker.Capabilities = capabilities
		worker.CachedContent = cachedContent
		worker.MaxParallel = registration.MaxParallel
		worker.PublicKey = registration.PublicKey
		worker.LastHeartbeat = now
		c.requeueLeasesHeldBy(id)
		return nil
	}
	c.workers[id] = &WorkerStatus{ID: id, Platform: registration.Platform, Capabilities: capabilities,
		CachedContent: cachedContent, MaxParallel: registration.MaxParallel, PublicKey: registration.PublicKey,
		RegisteredAt: now, LastHeartbeat: now}
	return nil
}

// WorkerStatus returns what the control plane currently knows about one
// registered worker.
func (c *ControlPlane) WorkerStatus(id string) (WorkerStatus, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	worker, ok := c.workers[id]
	if !ok {
		return WorkerStatus{}, false
	}
	copy := *worker
	copy.Capabilities = append([]string(nil), worker.Capabilities...)
	copy.CachedContent = append([]string(nil), worker.CachedContent...)
	return copy, true
}

// VerifyCompletionProvenance enforces that a worker which registered a
// PublicKey signs every lease it completes, and that the signature
// actually verifies against that key for this specific worker and
// lease - closing the gap a worker could otherwise exploit by simply
// omitting record, or by replaying a validly-signed record from a
// different lease or a different worker's completion.
//
// A worker that never registered a PublicKey is unaffected either way:
// this always succeeds for it, whether or not it sends a record, since
// there is nothing to verify the signature against.
func (c *ControlPlane) VerifyCompletionProvenance(workerID, leaseID string, record *provenance.ProvenanceRecord) error {
	status, ok := c.WorkerStatus(workerID)
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

func normalizedTokens(values []string, pattern *regexp.Regexp, limit int, label string) ([]string, error) {
	if len(values) > limit {
		return nil, typederrors.Newf(typederrors.CodeInvalidArgument, "control: too many %ss", label)
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !pattern.MatchString(value) {
			return nil, typederrors.Newf(typederrors.CodeInvalidArgument, "control: invalid %s %q", label, value)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

// requeueLeasesHeldBy moves every lease currently assigned to workerID back
// to pending. Callers must hold c.mu.
func (c *ControlPlane) requeueLeasesHeldBy(workerID string) {
	var reclaimed []string
	for _, lease := range c.leases {
		if lease.State == LeaseAssigned && lease.Worker == workerID {
			lease.State = LeasePending
			lease.Worker = ""
			reclaimed = append(reclaimed, lease.ID)
		}
	}
	sort.Strings(reclaimed)
	c.pending = append(c.pending, reclaimed...)
}

// Heartbeat refreshes a registered worker's liveness.
func (c *ControlPlane) Heartbeat(workerID string) error {
	if !workerIDPattern.MatchString(workerID) {
		return typederrors.New(typederrors.CodeInvalidArgument, "control: invalid worker id")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	worker, ok := c.workers[workerID]
	if !ok {
		return typederrors.Wrap(typederrors.CodeNotFound, "control: unknown worker",
			fmt.Errorf("%w: %q", ErrUnknownWorker, workerID))
	}
	worker.LastHeartbeat = c.now()
	return nil
}

// SubmitLease enqueues one unit of work, optionally restricted to workers
// reporting requiredPlatform (empty matches any), and returns its ID.
func (c *ControlPlane) SubmitLease(payload, requiredPlatform string) (string, error) {
	return c.SubmitLeaseWithRequirements(payload, requiredPlatform, nil, "")
}

// SubmitLeaseWithRequirements adds capability constraints and a preferred
// content digest used as a locality hint, never as an integrity assertion.
func (c *ControlPlane) SubmitLeaseWithRequirements(payload, requiredPlatform string, requiredCapabilities []string, preferredContent string) (string, error) {
	return c.SubmitLeaseWithTenant(payload, requiredPlatform, requiredCapabilities, preferredContent, "", 0)
}

// SubmitLeaseWithTenant adds opaque ownership and placement priority.
func (c *ControlPlane) SubmitLeaseWithTenant(payload, requiredPlatform string, requiredCapabilities []string, preferredContent, tenant string, priority int) (string, error) {
	if payload == "" || strings.TrimSpace(payload) == "" {
		return "", typederrors.New(typederrors.CodeInvalidArgument, "control: lease payload is required")
	}
	if len(payload) > maxOpaqueFieldBytes {
		return "", typederrors.New(typederrors.CodeInvalidArgument, "control: lease payload exceeds 1 MiB")
	}
	if requiredPlatform != "" && !platformPattern.MatchString(requiredPlatform) {
		return "", typederrors.New(typederrors.CodeInvalidArgument, "control: required platform must have the form os/architecture")
	}
	capabilities, err := normalizedTokens(requiredCapabilities, capabilityPattern, 64, "capability")
	if err != nil {
		return "", err
	}
	if preferredContent != "" && !contentPattern.MatchString(preferredContent) {
		return "", typederrors.New(typederrors.CodeInvalidArgument, "control: preferred content must be a sha256 digest")
	}
	if len(tenant) > 256 {
		return "", typederrors.New(typederrors.CodeInvalidArgument, "control: tenant exceeds 256 bytes")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := fmt.Sprintf("lease-%d", c.nextID)
	c.leases[id] = &Lease{ID: id, Payload: payload, RequiredPlatform: requiredPlatform,
		RequiredCapabilities: capabilities, PreferredContent: preferredContent,
		Tenant: tenant, Priority: priority, State: LeasePending}
	c.pending = append(c.pending, id)
	return id, nil
}

// NextLease assigns the best eligible pending lease. A false ok means no work.
func (c *ControlPlane) NextLease(workerID string) (Lease, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	worker, ok := c.workers[workerID]
	if !ok {
		return Lease{}, false, typederrors.Wrap(typederrors.CodeNotFound, "control: unknown worker",
			fmt.Errorf("%w: %q", ErrUnknownWorker, workerID))
	}
	active := 0
	for _, lease := range c.leases {
		if lease.State == LeaseAssigned && lease.Worker == workerID {
			active++
		}
	}
	if active >= worker.MaxParallel {
		return Lease{}, false, nil
	}
	candidates := make([]placement.Candidate, len(c.pending))
	for i, leaseID := range c.pending {
		lease := c.leases[leaseID]
		candidates[i] = placement.Candidate{
			RequiredPlatform:     lease.RequiredPlatform,
			RequiredCapabilities: lease.RequiredCapabilities,
			PreferredContent:     lease.PreferredContent,
			Priority:             lease.Priority,
		}
	}
	selected, ok := placement.Select(placement.Worker{
		Platform: worker.Platform, Capabilities: worker.Capabilities, CachedContent: worker.CachedContent,
	}, candidates)
	if ok {
		leaseID := c.pending[selected]
		lease := c.leases[leaseID]
		c.pending = append(c.pending[:selected:selected], c.pending[selected+1:]...)
		lease.State = LeaseAssigned
		lease.Worker = workerID
		lease.Attempt++
		lease.AssignedAt = c.now()
		return cloneLease(lease), true, nil
	}
	return Lease{}, false, nil
}

// CompleteLease commits a result only from the lease's current worker.
func (c *ControlPlane) CompleteLease(workerID, leaseID, result string) error {
	if !workerIDPattern.MatchString(workerID) {
		return typederrors.New(typederrors.CodeInvalidArgument, "control: invalid worker id")
	}
	if leaseID == "" || len(leaseID) > 128 {
		return typederrors.New(typederrors.CodeInvalidArgument, "control: invalid lease id")
	}
	if len(result) > maxOpaqueFieldBytes {
		return typederrors.New(typederrors.CodeInvalidArgument, "control: lease result exceeds 1 MiB")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	lease, ok := c.leases[leaseID]
	if !ok {
		return typederrors.Wrap(typederrors.CodeNotFound, "control: unknown lease",
			fmt.Errorf("%w: %q", ErrUnknownLease, leaseID))
	}
	if lease.State != LeaseAssigned || lease.Worker != workerID {
		return typederrors.Wrap(typederrors.CodeConflict, "control: lease not assigned to this worker",
			fmt.Errorf("%w: lease %q is %s, held by %q", ErrLeaseNotAssignedToWorker, leaseID, lease.State, lease.Worker))
	}
	lease.State = LeaseCompleted
	lease.Result = result
	lease.CompletedBy = workerID
	lease.CompletedAt = c.now()
	return nil
}

// CancelLease transitions pending or assigned work to a durable terminal
// state. The boolean is false when the same cancellation is replayed, making
// retries idempotent without emitting duplicate lifecycle events.
func (c *ControlPlane) CancelLease(actor, leaseID string) (bool, error) {
	if !workerIDPattern.MatchString(actor) {
		return false, typederrors.New(typederrors.CodeInvalidArgument, "control: invalid cancellation actor")
	}
	if leaseID == "" || len(leaseID) > 128 {
		return false, typederrors.New(typederrors.CodeInvalidArgument, "control: invalid lease id")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	lease, ok := c.leases[leaseID]
	if !ok {
		return false, typederrors.Wrap(typederrors.CodeNotFound, "control: unknown lease",
			fmt.Errorf("%w: %q", ErrUnknownLease, leaseID))
	}
	switch lease.State {
	case LeaseCanceled:
		return false, nil
	case LeaseCompleted:
		return false, typederrors.Wrap(typederrors.CodeConflict, "control: completed lease cannot be canceled",
			fmt.Errorf("%w: %q", ErrLeaseAlreadyCompleted, leaseID))
	case LeasePending:
		for index, id := range c.pending {
			if id == leaseID {
				c.pending = append(c.pending[:index:index], c.pending[index+1:]...)
				break
			}
		}
	case LeaseAssigned:
	default:
		return false, typederrors.Newf(typederrors.CodeInternal, "control: lease %q has invalid state %q", leaseID, lease.State)
	}
	lease.State = LeaseCanceled
	lease.CanceledBy = actor
	lease.CanceledAt = c.now()
	return true, nil
}

// Reap forgets expired workers and returns their requeued lease IDs.
func (c *ControlPlane) Reap(now time.Time) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var lost []string
	for id, worker := range c.workers {
		if now.Sub(worker.LastHeartbeat) <= c.heartbeatTimeout {
			continue
		}
		lost = append(lost, id)
		delete(c.workers, id)
	}
	if len(lost) == 0 {
		return nil
	}
	sort.Strings(lost)
	before := len(c.pending)
	for _, id := range lost {
		c.requeueLeasesHeldBy(id)
	}
	reaped := append([]string(nil), c.pending[before:]...)
	sort.Strings(reaped)
	return reaped
}

// LeaseStatus returns a snapshot of one lease.
func (c *ControlPlane) LeaseStatus(id string) (Lease, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lease, ok := c.leases[id]
	if !ok {
		return Lease{}, false
	}
	return cloneLease(lease), true
}

// Leases returns a deterministic immutable snapshot of every lease.
func (c *ControlPlane) Leases() []Lease {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]Lease, 0, len(c.leases))
	for _, lease := range c.leases {
		result = append(result, cloneLease(lease))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func cloneLease(lease *Lease) Lease {
	result := *lease
	result.RequiredCapabilities = append([]string(nil), lease.RequiredCapabilities...)
	return result
}

// WorkerStatuses returns a snapshot of every currently registered worker.
func (c *ControlPlane) WorkerStatuses() []WorkerStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]WorkerStatus, 0, len(c.workers))
	for _, worker := range c.workers {
		copy := *worker
		copy.Capabilities = append([]string(nil), worker.Capabilities...)
		copy.CachedContent = append([]string(nil), worker.CachedContent...)
		result = append(result, copy)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

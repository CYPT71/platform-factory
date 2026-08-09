package control

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	typederrors "github.com/CYPT71/secure-oci-base/internal/errors"
)

func TestRegisterWorkerRejectsEmptyFields(t *testing.T) {
	c := NewControlPlane(time.Minute)
	if err := c.RegisterWorker("", "linux/amd64"); err == nil {
		t.Fatal("accepted an empty worker id")
	}
	if err := c.RegisterWorker("worker-1", ""); err == nil {
		t.Fatal("accepted an empty platform")
	}
	if err := c.RegisterWorker("../worker", "linux/amd64"); err == nil {
		t.Fatal("accepted an unsafe worker id")
	}
	if err := c.RegisterWorker("worker-1", "linux"); err == nil {
		t.Fatal("accepted a malformed platform")
	}
}

func TestLeaseInputsAreBoundedAndValidated(t *testing.T) {
	c := NewControlPlane(time.Minute)
	if _, err := c.SubmitLease(" ", ""); err == nil {
		t.Fatal("accepted whitespace-only payload")
	}
	if _, err := c.SubmitLease("work", "../host"); err == nil {
		t.Fatal("accepted malformed required platform")
	}
	if _, err := c.SubmitLease(strings.Repeat("x", maxOpaqueFieldBytes+1), ""); err == nil {
		t.Fatal("accepted oversized payload")
	}
	if err := c.RegisterWorker("worker-1", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	id, err := c.SubmitLease("work", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.NextLease("worker-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteLease("worker-1", id, strings.Repeat("x", maxOpaqueFieldBytes+1)); err == nil {
		t.Fatal("accepted oversized result")
	}
}

func TestRegisterWorkerIsIdempotentAcrossRestarts(t *testing.T) {
	c := NewControlPlane(time.Minute)
	if err := c.RegisterWorker("worker-1", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterWorker("worker-1", "linux/arm64"); err != nil {
		t.Fatal(err)
	}
	statuses := c.WorkerStatuses()
	if len(statuses) != 1 || statuses[0].Platform != "linux/arm64" {
		t.Fatalf("statuses=%+v", statuses)
	}
}

// TestReRegistrationReclaimsAnInFlightLeaseWithoutWaitingForReap is a
// regression test for a real bug found running this package against an
// actual live kind Kubernetes cluster, not in a unit test: a worker's pod
// was killed mid-lease, and Kubernetes restarted it fast enough that the
// new process's Register call refreshed LastHeartbeat for the same
// worker ID before the heartbeat timeout ever elapsed. Reap saw a
// perfectly healthy heartbeat and never fired, so the lease the old,
// now-dead process had been holding stayed "assigned" forever - visible
// on the real cluster as a lease stuck at state "assigned" for over 20
// seconds while a brand new worker-2 pod sat idle. A real client only
// calls Register once per process lifetime (Heartbeat is what a live
// process sends repeatedly), so a second Register for an ID already known
// is itself proof the previous holder is gone, and must reclaim its
// leases immediately rather than waiting for a heartbeat timeout that the
// restarted process's own heartbeats will keep postponing forever.
func TestReRegistrationReclaimsAnInFlightLeaseWithoutWaitingForReap(t *testing.T) {
	c := NewControlPlane(time.Minute)
	if err := c.RegisterWorker("worker-1", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterWorker("worker-2", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	leaseID, err := c.SubmitLease("build the thing", "")
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := c.NextLease("worker-2")
	if err != nil || !ok || lease.ID != leaseID {
		t.Fatalf("lease=%+v ok=%v err=%v", lease, ok, err)
	}

	// worker-2's pod is killed and Kubernetes starts a fresh one under
	// the same identity, well before any heartbeat timeout - Reap alone
	// would never see this.
	if err := c.RegisterWorker("worker-2", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if status, ok := c.LeaseStatus(leaseID); !ok || status.State != LeasePending || status.Worker != "" {
		t.Fatalf("lease was not reclaimed on re-registration: %+v ok=%v", status, ok)
	}

	// The old (dead) process's late completion attempt is still rejected,
	// exactly like the Reap path.
	if err := c.CompleteLease("worker-2", leaseID, "late, from the dead process"); !errors.Is(err, ErrLeaseNotAssignedToWorker) {
		t.Fatalf("late completion from the reclaimed lease was not rejected: %v", err)
	}

	// The new process (also identified as worker-2) can now correctly
	// pick the lease back up and complete it.
	lease, ok, err = c.NextLease("worker-1")
	if err != nil || !ok || lease.ID != leaseID || lease.Attempt != 2 {
		t.Fatalf("second attempt: lease=%+v ok=%v err=%v", lease, ok, err)
	}
	if err := c.CompleteLease("worker-1", leaseID, "done for real"); err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatRejectsUnknownWorker(t *testing.T) {
	c := NewControlPlane(time.Minute)
	if err := c.Heartbeat("ghost"); !errors.Is(err, ErrUnknownWorker) {
		t.Fatalf("err=%v", err)
	}
}

func TestNextLeaseRejectsUnknownWorker(t *testing.T) {
	c := NewControlPlane(time.Minute)
	if _, _, err := c.NextLease("ghost"); !errors.Is(err, ErrUnknownWorker) {
		t.Fatalf("err=%v", err)
	}
}

func TestNextLeaseReturnsNothingWhenQueueIsEmpty(t *testing.T) {
	c := NewControlPlane(time.Minute)
	if err := c.RegisterWorker("worker-1", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	_, ok, err := c.NextLease("worker-1")
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestLeaseAssignmentIsPlatformAware(t *testing.T) {
	c := NewControlPlane(time.Minute)
	if err := c.RegisterWorker("amd64-worker", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterWorker("arm64-worker", "linux/arm64"); err != nil {
		t.Fatal(err)
	}
	armOnly, err := c.SubmitLease("build for arm64", "linux/arm64")
	if err != nil {
		t.Fatal(err)
	}
	any, err := c.SubmitLease("build for any platform", "")
	if err != nil {
		t.Fatal(err)
	}

	// The amd64 worker must skip the arm64-only lease and pull the
	// platform-agnostic one instead, not block behind it.
	lease, ok, err := c.NextLease("amd64-worker")
	if err != nil || !ok || lease.ID != any {
		t.Fatalf("lease=%+v ok=%v err=%v", lease, ok, err)
	}
	if _, ok, _ := c.NextLease("amd64-worker"); ok {
		t.Fatal("amd64 worker was handed the arm64-only lease")
	}
	lease, ok, err = c.NextLease("arm64-worker")
	if err != nil || !ok || lease.ID != armOnly {
		t.Fatalf("lease=%+v ok=%v err=%v", lease, ok, err)
	}
}

// TestAmd64AndArm64WorkersMakeProgressConcurrently is the genuinely
// parallel counterpart to TestLeaseAssignmentIsPlatformAware, which only
// proves platform routing correctness across sequential NextLease calls.
// Here two workers of different platforms poll for and complete leases
// from real goroutines running at the same time against one shared
// ControlPlane, under -race, so both the platform-isolation guarantee
// (each worker only ever receives its own platform's leases) and basic
// thread safety under real concurrent access are exercised together -
// not just implied by ControlPlane's own internal mutex.
func TestAmd64AndArm64WorkersMakeProgressConcurrently(t *testing.T) {
	c := NewControlPlane(time.Minute)
	if err := c.RegisterWorker("amd64-worker", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterWorker("arm64-worker", "linux/arm64"); err != nil {
		t.Fatal(err)
	}

	const leasesPerPlatform = 50
	for i := 0; i < leasesPerPlatform; i++ {
		if _, err := c.SubmitLease(fmt.Sprintf("amd64 build %d", i), "linux/amd64"); err != nil {
			t.Fatal(err)
		}
		if _, err := c.SubmitLease(fmt.Sprintf("arm64 build %d", i), "linux/arm64"); err != nil {
			t.Fatal(err)
		}
	}

	poll := func(workerID, platform string, completed *int64, mu *sync.Mutex) {
		for {
			lease, ok, err := c.NextLease(workerID)
			if err != nil {
				t.Errorf("%s: NextLease: %v", workerID, err)
				return
			}
			if !ok {
				return
			}
			if lease.RequiredPlatform != "" && lease.RequiredPlatform != platform {
				t.Errorf("%s (platform %s) received a lease requiring %q", workerID, platform, lease.RequiredPlatform)
			}
			if err := c.CompleteLease(workerID, lease.ID, "ok"); err != nil {
				t.Errorf("%s: CompleteLease: %v", workerID, err)
				return
			}
			mu.Lock()
			*completed++
			mu.Unlock()
		}
	}

	var amd64Completed, arm64Completed int64
	var amd64Mu, arm64Mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); poll("amd64-worker", "linux/amd64", &amd64Completed, &amd64Mu) }()
	go func() { defer wg.Done(); poll("arm64-worker", "linux/arm64", &arm64Completed, &arm64Mu) }()
	wg.Wait()

	if amd64Completed != leasesPerPlatform || arm64Completed != leasesPerPlatform {
		t.Fatalf("amd64Completed=%d arm64Completed=%d, want %d each",
			amd64Completed, arm64Completed, leasesPerPlatform)
	}
}

func TestLeaseAssignmentUsesCapabilitiesLoadAndCacheLocality(t *testing.T) {
	c := NewControlPlane(time.Minute)
	digest := "sha256:" + strings.Repeat("a", 64)
	if err := c.RegisterWorkerWithOptions("worker-1", WorkerRegistration{
		Platform: "linux/amd64", Capabilities: []string{"kvm", "network"},
		CachedContent: []string{digest}, MaxParallel: 2,
	}); err != nil {
		t.Fatal(err)
	}
	oldest, err := c.SubmitLeaseWithRequirements("uncached", "linux/amd64", []string{"kvm"}, "sha256:"+strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	local, err := c.SubmitLeaseWithRequirements("cached", "linux/amd64", []string{"network", "kvm"}, digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SubmitLeaseWithRequirements("unsupported", "linux/amd64", []string{"gpu"}, ""); err != nil {
		t.Fatal(err)
	}

	lease, ok, err := c.NextLease("worker-1")
	if err != nil || !ok || lease.ID != local {
		t.Fatalf("cache-local assignment: lease=%+v ok=%v err=%v", lease, ok, err)
	}
	lease, ok, err = c.NextLease("worker-1")
	if err != nil || !ok || lease.ID != oldest {
		t.Fatalf("fallback assignment: lease=%+v ok=%v err=%v", lease, ok, err)
	}
	if _, ok, err := c.NextLease("worker-1"); err != nil || ok {
		t.Fatalf("worker exceeded max parallel: ok=%v err=%v", ok, err)
	}
	if err := c.CompleteLease("worker-1", local, "done"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.NextLease("worker-1"); err != nil || ok {
		t.Fatalf("worker received unsupported lease: ok=%v err=%v", ok, err)
	}
}

// TestLeaseAssignmentPrefersHigherPriorityTenant proves priority (set at
// submission via SubmitLeaseWithTenant, the way
// cmd/platform-factory-control-plane resolves it from
// internal/quota.FairScheduler.GetPriority) actually reaches NextLease's
// placement decision end to end through ControlPlane, not just in
// internal/placement's own isolated unit tests.
func TestLeaseAssignmentPrefersHigherPriorityTenant(t *testing.T) {
	c := NewControlPlane(time.Minute)
	if err := c.RegisterWorkerWithOptions("worker-1", WorkerRegistration{
		Platform: "linux/amd64", MaxParallel: 2,
	}); err != nil {
		t.Fatal(err)
	}
	low, err := c.SubmitLeaseWithTenant("low-priority-work", "linux/amd64", nil, "", "tenant-standard", 0)
	if err != nil {
		t.Fatal(err)
	}
	high, err := c.SubmitLeaseWithTenant("high-priority-work", "linux/amd64", nil, "", "tenant-premium", 10)
	if err != nil {
		t.Fatal(err)
	}

	lease, ok, err := c.NextLease("worker-1")
	if err != nil || !ok || lease.ID != high {
		t.Fatalf("want the higher-priority lease first: lease=%+v ok=%v err=%v (low=%q high=%q)", lease, ok, err, low, high)
	}
	if lease.Tenant != "tenant-premium" || lease.Priority != 10 {
		t.Fatalf("assigned lease lost its tenant/priority: %+v", lease)
	}

	lease, ok, err = c.NextLease("worker-1")
	if err != nil || !ok || lease.ID != low {
		t.Fatalf("want the lower-priority lease second: lease=%+v ok=%v err=%v", lease, ok, err)
	}
	if lease.Tenant != "tenant-standard" {
		t.Fatalf("assigned lease lost its tenant: %+v", lease)
	}
}

// TestSubmitLeaseWithRequirementsDefaultsToZeroPriorityAndNoTenant proves
// the pre-tenant call path is completely unaffected by this feature.
func TestSubmitLeaseWithRequirementsDefaultsToZeroPriorityAndNoTenant(t *testing.T) {
	c := NewControlPlane(time.Minute)
	if err := c.RegisterWorkerWithOptions("worker-1", WorkerRegistration{Platform: "linux/amd64", MaxParallel: 1}); err != nil {
		t.Fatal(err)
	}
	id, err := c.SubmitLeaseWithRequirements("work", "linux/amd64", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := c.NextLease("worker-1")
	if err != nil || !ok || lease.ID != id {
		t.Fatalf("lease=%+v ok=%v err=%v", lease, ok, err)
	}
	if lease.Tenant != "" || lease.Priority != 0 {
		t.Fatalf("expected no tenant and zero priority, got %+v", lease)
	}
}

func TestSubmitLeaseWithTenantRejectsOversizedTenant(t *testing.T) {
	c := NewControlPlane(time.Minute)
	oversized := strings.Repeat("a", 257)
	if _, err := c.SubmitLeaseWithTenant("work", "linux/amd64", nil, "", oversized, 0); err == nil {
		t.Fatal("expected an oversized tenant to be rejected")
	}
}

func TestSchedulingMetadataFailsClosed(t *testing.T) {
	c := NewControlPlane(time.Minute)
	if err := c.RegisterWorkerWithOptions("worker-1", WorkerRegistration{Platform: "linux/amd64", MaxParallel: 0}); err == nil {
		t.Fatal("accepted zero max parallel")
	}
	if err := c.RegisterWorkerWithOptions("worker-1", WorkerRegistration{Platform: "linux/amd64", Capabilities: []string{"../host"}, MaxParallel: 1}); err == nil {
		t.Fatal("accepted malformed capability")
	}
	if _, err := c.SubmitLeaseWithRequirements("work", "", nil, "sha256:nope"); err == nil {
		t.Fatal("accepted malformed preferred content digest")
	}
}

func TestSchedulingSnapshotsDoNotExposeMutableInternalSlices(t *testing.T) {
	c := NewControlPlane(time.Minute)
	if err := c.RegisterWorkerWithOptions("worker-1", WorkerRegistration{
		Platform: "linux/amd64", Capabilities: []string{"kvm"}, MaxParallel: 1,
	}); err != nil {
		t.Fatal(err)
	}
	id, err := c.SubmitLeaseWithRequirements("work", "", []string{"kvm"}, "")
	if err != nil {
		t.Fatal(err)
	}
	workers := c.WorkerStatuses()
	workers[0].Capabilities[0] = "gpu"
	lease, _ := c.LeaseStatus(id)
	lease.RequiredCapabilities[0] = "gpu"
	if got := c.WorkerStatuses()[0].Capabilities[0]; got != "kvm" {
		t.Fatalf("worker snapshot mutated internal capability: %q", got)
	}
	if got, _ := c.LeaseStatus(id); got.RequiredCapabilities[0] != "kvm" {
		t.Fatalf("lease snapshot mutated internal requirement: %+v", got)
	}
}

func TestCompleteLeaseRejectsWrongWorkerOrState(t *testing.T) {
	c := NewControlPlane(time.Minute)
	if err := c.RegisterWorker("worker-1", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterWorker("worker-2", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	id, err := c.SubmitLease("payload", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteLease("worker-1", id, "result"); !errors.Is(err, ErrLeaseNotAssignedToWorker) {
		t.Fatalf("completing a still-pending lease: err=%v", err)
	}
	if _, _, err := c.NextLease("worker-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteLease("worker-2", id, "result"); !errors.Is(err, ErrLeaseNotAssignedToWorker) {
		t.Fatalf("completing another worker's lease: err=%v", err)
	}
	if err := c.CompleteLease("worker-1", id, "result"); err != nil {
		t.Fatal(err)
	}
	completed, ok := c.LeaseStatus(id)
	if !ok || completed.CompletedBy != "worker-1" || completed.CompletedAt.IsZero() {
		t.Fatalf("completion provenance missing: %+v ok=%v", completed, ok)
	}
	if err := c.CompleteLease("worker-1", id, "again"); !errors.Is(err, ErrLeaseNotAssignedToWorker) {
		t.Fatalf("re-completing an already-completed lease: err=%v", err)
	}
}

func TestCompleteLeaseRejectsUnknownLease(t *testing.T) {
	c := NewControlPlane(time.Minute)
	if err := c.CompleteLease("worker-1", "nope", "result"); !errors.Is(err, ErrUnknownLease) {
		t.Fatalf("err=%v", err)
	}
}

func TestCancelLeaseIsTerminalDurableAndIdempotent(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	c := NewControlPlane(time.Minute)
	c.now = func() time.Time { return start }
	if err := c.RegisterWorker("worker-a", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	pendingID, _ := c.SubmitLease("pending", "")
	assignedID, _ := c.SubmitLease("assigned", "")
	if changed, err := c.CancelLease("operator", pendingID); err != nil || !changed {
		t.Fatalf("cancel pending: changed=%v err=%v", changed, err)
	}
	lease, ok, err := c.NextLease("worker-a")
	if err != nil || !ok || lease.ID != assignedID {
		t.Fatalf("next=%+v ok=%v err=%v", lease, ok, err)
	}
	if changed, err := c.CancelLease("operator", assignedID); err != nil || !changed {
		t.Fatalf("cancel assigned: changed=%v err=%v", changed, err)
	}
	if changed, err := c.CancelLease("operator", assignedID); err != nil || changed {
		t.Fatalf("replayed cancel: changed=%v err=%v", changed, err)
	}
	status, _ := c.LeaseStatus(assignedID)
	if status.State != LeaseCanceled || status.CanceledBy != "operator" || !status.CanceledAt.Equal(start) {
		t.Fatalf("status=%+v", status)
	}
	if err := c.CompleteLease("worker-a", assignedID, "late"); !errors.Is(err, ErrLeaseNotAssignedToWorker) {
		t.Fatalf("late completion err=%v", err)
	}
	completedID, _ := c.SubmitLease("completed", "")
	completed, _, _ := c.NextLease("worker-a")
	if completed.ID != completedID {
		t.Fatalf("completed assignment=%+v", completed)
	}
	if err := c.CompleteLease("worker-a", completedID, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CancelLease("operator", completedID); !errors.Is(err, ErrLeaseAlreadyCompleted) {
		t.Fatalf("cancel completed err=%v", err)
	}
}

// TestReapReassignsALostWorkersLeaseIdempotently is the core proof this
// package exists for: a worker that goes silent has its in-flight lease
// requeued and picked up by a different worker, and if the original
// worker later tries to report a result anyway (a real possibility - it
// might just have been slow, not actually crashed), that late completion
// is rejected rather than silently racing with, or overwriting, the
// second worker's own completion.
func TestReapReassignsALostWorkersLeaseIdempotently(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewControlPlane(30 * time.Second)
	c.now = func() time.Time { return start }

	if err := c.RegisterWorker("worker-a", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterWorker("worker-b", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	leaseID, err := c.SubmitLease("build the thing", "")
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := c.NextLease("worker-a")
	if err != nil || !ok || lease.ID != leaseID {
		t.Fatalf("lease=%+v ok=%v err=%v", lease, ok, err)
	}

	// worker-a goes silent; time passes well beyond the heartbeat
	// timeout without a heartbeat, while worker-b keeps checking in.
	later := start.Add(time.Minute)
	c.now = func() time.Time { return later }
	if err := c.Heartbeat("worker-b"); err != nil {
		t.Fatal(err)
	}

	reaped := c.Reap(later)
	if len(reaped) != 1 || reaped[0] != leaseID {
		t.Fatalf("reaped=%v", reaped)
	}
	if status, ok := c.LeaseStatus(leaseID); !ok || status.State != LeasePending {
		t.Fatalf("status=%+v ok=%v", status, ok)
	}

	// worker-b now correctly picks up the reassigned lease.
	lease, ok, err = c.NextLease("worker-b")
	if err != nil || !ok || lease.ID != leaseID || lease.Attempt != 2 {
		t.Fatalf("second attempt: lease=%+v ok=%v err=%v", lease, ok, err)
	}

	// worker-a - unaware it was reaped - now tries to report the result
	// of the work it was originally assigned. This must not succeed.
	if err := c.CompleteLease("worker-a", leaseID, "late result"); !errors.Is(err, ErrLeaseNotAssignedToWorker) {
		t.Fatalf("late completion from the reaped worker was not rejected: err=%v", err)
	}

	if err := c.CompleteLease("worker-b", leaseID, "correct result"); err != nil {
		t.Fatal(err)
	}
	status, ok := c.LeaseStatus(leaseID)
	if !ok || status.State != LeaseCompleted || status.Result != "correct result" || status.Worker != "worker-b" {
		t.Fatalf("final status=%+v ok=%v", status, ok)
	}

	// worker-a was forgotten by Reap; it must register again to act
	// further, exactly like a real restarted process would.
	if err := c.Heartbeat("worker-a"); !errors.Is(err, ErrUnknownWorker) {
		t.Fatalf("reaped worker still known: err=%v", err)
	}
}

func TestReapLeavesHealthyWorkersAndUnassignedLeasesAlone(t *testing.T) {
	start := time.Now()
	c := NewControlPlane(30 * time.Second)
	c.now = func() time.Time { return start }
	if err := c.RegisterWorker("worker-a", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SubmitLease("untouched", ""); err != nil {
		t.Fatal(err)
	}
	if reaped := c.Reap(start.Add(10 * time.Second)); reaped != nil {
		t.Fatalf("reaped a healthy worker: %v", reaped)
	}
	if statuses := c.WorkerStatuses(); len(statuses) != 1 {
		t.Fatalf("statuses=%+v", statuses)
	}
}

// TestConcurrentWorkersAndLeasesNeverDoubleAssignOrLoseALease drives many
// workers and leases through the whole lifecycle concurrently under
// -race, and checks the invariant that matters most for a distributed
// scheduler: every lease ends up completed by exactly one worker, never
// zero and never more than one.
func TestConcurrentWorkersAndLeasesNeverDoubleAssignOrLoseALease(t *testing.T) {
	const workerCount = 8
	const leaseCount = 200
	c := NewControlPlane(time.Minute)

	for i := 0; i < workerCount; i++ {
		if err := c.RegisterWorker(fmt.Sprintf("worker-%d", i), "linux/amd64"); err != nil {
			t.Fatal(err)
		}
	}
	leaseIDs := make([]string, leaseCount)
	for i := range leaseIDs {
		id, err := c.SubmitLease(fmt.Sprintf("payload-%d", i), "")
		if err != nil {
			t.Fatal(err)
		}
		leaseIDs[i] = id
	}

	var wg sync.WaitGroup
	completions := make(chan string, leaseCount)
	for i := 0; i < workerCount; i++ {
		workerID := fmt.Sprintf("worker-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				lease, ok, err := c.NextLease(workerID)
				if err != nil {
					t.Error(err)
					return
				}
				if !ok {
					return
				}
				if err := c.CompleteLease(workerID, lease.ID, "done"); err != nil {
					t.Error(err)
					return
				}
				completions <- lease.ID
			}
		}()
	}
	wg.Wait()
	close(completions)

	seen := map[string]int{}
	for id := range completions {
		seen[id]++
	}
	for _, id := range leaseIDs {
		if seen[id] != 1 {
			t.Fatalf("lease %q was completed %d times, want exactly 1", id, seen[id])
		}
	}
}

// TestErrorsAreTypedAndPreserveSentinelCompatibility proves both halves of
// the internal/errors migration at once: every error this package returns
// now carries a programmatically-checkable code, and the pre-existing
// sentinel-based errors.Is API (cmd/platform-factory-control-plane/server.go's
// dispatch on ErrUnknownWorker/ErrUnknownLease/ErrLeaseNotAssignedToWorker/
// ErrLeaseAlreadyCompleted) still resolves through the typed wrap - the
// sentinels are unwrapped, not replaced.
func TestErrorsAreTypedAndPreserveSentinelCompatibility(t *testing.T) {
	c := NewControlPlane(time.Minute)

	if err := c.RegisterWorker("", "linux/amd64"); !typederrors.HasCode(err, typederrors.CodeInvalidArgument) {
		t.Fatalf("RegisterWorker validation: got code %q, want %q (err=%v)", typederrors.GetErrorCode(err), typederrors.CodeInvalidArgument, err)
	}

	err := c.Heartbeat("never-registered")
	if !errors.Is(err, ErrUnknownWorker) {
		t.Fatalf("Heartbeat: errors.Is did not find ErrUnknownWorker through the typed wrap (err=%v)", err)
	}
	if !typederrors.HasCode(err, typederrors.CodeNotFound) {
		t.Fatalf("Heartbeat: got code %q, want %q (err=%v)", typederrors.GetErrorCode(err), typederrors.CodeNotFound, err)
	}

	if err := c.RegisterWorker("worker-1", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteLease("worker-1", "no-such-lease", "done"); !errors.Is(err, ErrUnknownLease) ||
		!typederrors.HasCode(err, typederrors.CodeNotFound) {
		t.Fatalf("CompleteLease unknown lease: err=%v code=%q", err, typederrors.GetErrorCode(err))
	}

	leaseID, err := c.SubmitLease("work", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.NextLease("worker-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteLease("worker-1", leaseID, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CancelLease("worker-1", leaseID); !errors.Is(err, ErrLeaseAlreadyCompleted) ||
		!typederrors.HasCode(err, typederrors.CodeConflict) {
		t.Fatalf("CancelLease already-completed: err=%v code=%q", err, typederrors.GetErrorCode(err))
	}

	leaseID2, err := c.SubmitLease("more work", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.NextLease("worker-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterWorker("worker-1", "linux/amd64"); err != nil {
		t.Fatal(err) // re-registration requeues the lease, orphaning worker-1's assignment
	}
	if err := c.CompleteLease("worker-1", leaseID2, "late"); !errors.Is(err, ErrLeaseNotAssignedToWorker) ||
		!typederrors.HasCode(err, typederrors.CodeConflict) {
		t.Fatalf("CompleteLease not assigned: err=%v code=%q", err, typederrors.GetErrorCode(err))
	}
}

package migration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/idempotency"
	domainmigration "github.com/CYPT71/platform-factory/internal/migration"
)

type reconciliationTarget struct {
	distance       uint64
	decrement      uint64
	fingerprint    func(uint64) string
	applyErr       error
	unknown        bool
	observeErr     error
	differenceErr  error
	applyCount     int
	effectCount    int
	operationIDs   []core.OperationID
	cancelOnApply  context.CancelFunc
	waitForContext bool
	journal        core.OperationJournal
}

func (t *reconciliationTarget) Observe(context.Context, domainmigration.Resource) (TargetObservation, error) {
	return TargetObservation{Native: t.distance}, t.observeErr
}

func (t *reconciliationTarget) Apply(ctx context.Context, step domainmigration.Step, _ domainmigration.Resource) error {
	t.applyCount++
	t.operationIDs = append(t.operationIDs, step.OperationID)
	if t.journal != nil {
		started, err := t.journal.Start(step.OperationID, "migration-reconcile-test")
		if err != nil {
			return err
		}
		if !started {
			return nil
		}
		defer func() { _ = t.journal.Complete(step.OperationID) }()
	}
	if t.waitForContext {
		<-ctx.Done()
		return ctx.Err()
	}
	if t.decrement >= t.distance {
		t.distance = 0
	} else {
		t.distance -= t.decrement
	}
	t.effectCount++
	if t.cancelOnApply != nil {
		t.cancelOnApply()
	}
	return t.applyErr
}

func (*reconciliationTarget) Verify(_ context.Context, _ domainmigration.Resource, observation TargetObservation) (bool, error) {
	distance, ok := observation.Native.(uint64)
	return ok && distance == 0, nil
}

func (t *reconciliationTarget) Difference(_ context.Context, _ domainmigration.Resource, observation TargetObservation) (TargetDifference, error) {
	if t.differenceErr != nil {
		return TargetDifference{}, t.differenceErr
	}
	distance, _ := observation.Native.(uint64)
	fingerprint := fmt.Sprintf("distance-%d", distance)
	if t.fingerprint != nil {
		fingerprint = t.fingerprint(distance)
	}
	return TargetDifference{Known: !t.unknown, Distance: distance, Fingerprint: fingerprint}, nil
}

func reconciliationExecutor(target TargetOperations, sink *evidenceSink) *Executor {
	return NewExecutor(NewCapabilityResolver(executionCandidates{}), fakeFactory{operations: target}, sink)
}

func TestReconcileConvergesWithBoundedDeterministicActions(t *testing.T) {
	target := &reconciliationTarget{distance: 3, decrement: 1}
	plan := executionPlan(t, false)
	result, err := reconciliationExecutor(target, &evidenceSink{}).Reconcile(tracedContext(), plan, ReconciliationOptions{MaxIterations: 4})
	if err != nil || len(result.Evidence) != 1 || result.Evidence[0].Status != StepConverged || target.applyCount != 3 {
		t.Fatalf("result=%+v applies=%d err=%v", result, target.applyCount, err)
	}
	if !result.Evidence[0].Verified || result.Evidence[0].ObservationCount != 6 || result.Evidence[0].VerificationCount != 6 {
		t.Fatalf("incomplete observed evidence=%+v", result.Evidence[0])
	}
	if target.operationIDs[0] == target.operationIDs[1] || target.operationIDs[1] == target.operationIDs[2] {
		t.Fatalf("reconciliation action IDs are not unique: %v", target.operationIDs)
	}
	target2 := &reconciliationTarget{distance: 3, decrement: 1}
	if _, err := reconciliationExecutor(target2, &evidenceSink{}).Reconcile(tracedContext(), plan, ReconciliationOptions{MaxIterations: 4}); err != nil {
		t.Fatal(err)
	}
	for i := range target.operationIDs {
		if target.operationIDs[i] != target2.operationIDs[i] {
			t.Fatalf("action IDs are not deterministic: %v != %v", target.operationIDs, target2.operationIDs)
		}
	}
}

func TestReconcileRejectsNoProgressAndOscillation(t *testing.T) {
	t.Run("no progress", func(t *testing.T) {
		target := &reconciliationTarget{distance: 2}
		result, err := reconciliationExecutor(target, &evidenceSink{}).Reconcile(tracedContext(), executionPlan(t, false), ReconciliationOptions{MaxIterations: 3})
		if !errors.Is(err, ErrReconciliationNoProgress) || result.Evidence[0].Status != StepFailed || target.applyCount != 1 {
			t.Fatalf("result=%+v applies=%d err=%v", result, target.applyCount, err)
		}
	})
	t.Run("oscillation", func(t *testing.T) {
		target := &reconciliationTarget{distance: 3, decrement: 1, fingerprint: func(uint64) string { return "repeated-state" }}
		_, err := reconciliationExecutor(target, &evidenceSink{}).Reconcile(tracedContext(), executionPlan(t, false), ReconciliationOptions{MaxIterations: 3})
		if !errors.Is(err, ErrReconciliationOscillation) || target.applyCount != 1 {
			t.Fatalf("applies=%d err=%v", target.applyCount, err)
		}
	})
}

func TestReconcileLimitUnknownIndeterminateAndCancellation(t *testing.T) {
	t.Run("limit", func(t *testing.T) {
		target := &reconciliationTarget{distance: 5, decrement: 1}
		_, err := reconciliationExecutor(target, &evidenceSink{}).Reconcile(tracedContext(), executionPlan(t, false), ReconciliationOptions{MaxIterations: 2})
		if !errors.Is(err, ErrReconciliationLimit) || target.applyCount != 2 {
			t.Fatalf("applies=%d err=%v", target.applyCount, err)
		}
	})
	t.Run("unknown partial observation", func(t *testing.T) {
		target := &reconciliationTarget{distance: 2, decrement: 1, unknown: true}
		_, err := reconciliationExecutor(target, &evidenceSink{}).Reconcile(tracedContext(), executionPlan(t, false), ReconciliationOptions{})
		if !errors.Is(err, ErrReconciliationUnknown) || target.applyCount != 0 {
			t.Fatalf("applies=%d err=%v", target.applyCount, err)
		}
	})
	t.Run("indeterminate observed to convergence", func(t *testing.T) {
		target := &reconciliationTarget{distance: 1, decrement: 1, applyErr: core.ErrOperationIndeterminate}
		result, err := reconciliationExecutor(target, &evidenceSink{}).Reconcile(tracedContext(), executionPlan(t, false), ReconciliationOptions{})
		if err != nil || !result.Evidence[0].Indeterminate || result.Evidence[0].Status != StepConverged || target.applyCount != 1 {
			t.Fatalf("result=%+v applies=%d err=%v", result, target.applyCount, err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(tracedContext())
		target := &reconciliationTarget{distance: 2, decrement: 1, cancelOnApply: cancel}
		_, err := reconciliationExecutor(target, &evidenceSink{}).Reconcile(ctx, executionPlan(t, false), ReconciliationOptions{})
		if !errors.Is(err, context.Canceled) || target.applyCount != 1 {
			t.Fatalf("applies=%d err=%v", target.applyCount, err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(tracedContext(), 10*time.Millisecond)
		defer cancel()
		target := &reconciliationTarget{distance: 2, decrement: 1, waitForContext: true}
		_, err := reconciliationExecutor(target, &evidenceSink{}).Reconcile(ctx, executionPlan(t, false), ReconciliationOptions{})
		if !errors.Is(err, context.DeadlineExceeded) || target.applyCount != 1 {
			t.Fatalf("applies=%d err=%v", target.applyCount, err)
		}
	})
}

func TestReconcileRejectsInvalidConfigurationAndMissingProgressPort(t *testing.T) {
	plan := executionPlan(t, false)
	if _, err := reconciliationExecutor(newFakeTarget(), &evidenceSink{}).Reconcile(tracedContext(), plan, ReconciliationOptions{}); err == nil {
		t.Fatal("expected missing progress port rejection")
	}
	if _, err := reconciliationExecutor(&reconciliationTarget{}, &evidenceSink{}).Reconcile(tracedContext(), plan, ReconciliationOptions{MaxIterations: -1}); err == nil {
		t.Fatal("expected invalid iteration limit rejection")
	}
}

func TestReconcileAlreadyConvergedAndObservationFailures(t *testing.T) {
	t.Run("already converged", func(t *testing.T) {
		target := &reconciliationTarget{}
		result, err := reconciliationExecutor(target, &evidenceSink{}).Reconcile(tracedContext(), executionPlan(t, false), ReconciliationOptions{})
		if err != nil || result.Evidence[0].Status != StepConverged || target.applyCount != 0 {
			t.Fatalf("result=%+v applies=%d err=%v", result, target.applyCount, err)
		}
	})
	t.Run("observe failure", func(t *testing.T) {
		target := &reconciliationTarget{distance: 2, observeErr: errors.New("partial native observation")}
		result, err := reconciliationExecutor(target, &evidenceSink{}).Reconcile(tracedContext(), executionPlan(t, false), ReconciliationOptions{})
		if err == nil || result.Evidence[0].Status != StepFailed || target.applyCount != 0 || result.Evidence[0].ObservationCount != 0 || result.Evidence[0].VerificationCount != 0 {
			t.Fatalf("result=%+v applies=%d err=%v", result, target.applyCount, err)
		}
	})
	t.Run("difference failure", func(t *testing.T) {
		target := &reconciliationTarget{distance: 2, differenceErr: errors.New("normalization failed")}
		result, err := reconciliationExecutor(target, &evidenceSink{}).Reconcile(tracedContext(), executionPlan(t, false), ReconciliationOptions{})
		if err == nil || result.Evidence[0].Status != StepFailed || target.applyCount != 0 {
			t.Fatalf("result=%+v applies=%d err=%v", result, target.applyCount, err)
		}
	})
}

func TestReconcileOperationIdentitySurvivesRestartAndJournalReplay(t *testing.T) {
	plan := executionPlan(t, false)
	journal := idempotency.NewMemoryJournal()
	first := &reconciliationTarget{distance: 3, decrement: 1, journal: journal}
	_, err := reconciliationExecutor(first, &evidenceSink{}).Reconcile(tracedContext(), plan, ReconciliationOptions{MaxIterations: 1})
	if !errors.Is(err, ErrReconciliationLimit) || first.effectCount != 1 {
		t.Fatalf("first effects=%d err=%v", first.effectCount, err)
	}

	// Simulate restart before retaining the post-action observation. The same
	// canonical difference must replay the same journal identity.
	restarted := &reconciliationTarget{distance: 3, decrement: 1, journal: journal}
	_, err = reconciliationExecutor(restarted, &evidenceSink{}).Reconcile(tracedContext(), plan, ReconciliationOptions{MaxIterations: 1})
	if !errors.Is(err, ErrReconciliationNoProgress) || restarted.effectCount != 0 || restarted.applyCount != 1 {
		t.Fatalf("restarted effects=%d applies=%d err=%v", restarted.effectCount, restarted.applyCount, err)
	}
	if first.operationIDs[0] != restarted.operationIDs[0] {
		t.Fatalf("restart changed operation identity: %q != %q", first.operationIDs[0], restarted.operationIDs[0])
	}
}

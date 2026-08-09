package migration

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/core"
	domainmigration "github.com/CYPT71/secure-oci-base/internal/migration"
	"github.com/CYPT71/secure-oci-base/internal/observability"
)

type executionCandidates struct{}

func (executionCandidates) Candidates(context.Context, CapabilityRequirement) ([]CapabilityCandidate, error) {
	return []CapabilityCandidate{{ID: "target", Digest: testDigest('d'), Capability: "compute", Version: "v1", Compatibility: CompatibilityDirect, Evidence: CapabilityEvidence{Declared: true, Discovered: true, Negotiated: true, Verified: true, Available: true}}}, nil
}

type fakeFactory struct {
	operations TargetOperations
	err        error
}

func (f fakeFactory) Open(_ context.Context, _ ResolvedCapability) (TargetOperations, error) {
	return f.operations, f.err
}

type fakeTarget struct {
	mu          sync.Mutex
	converged   map[string]bool
	applyErrors map[string]error
	observeErrs map[string]error
	verifyErrs  map[string]error
	applies     map[core.OperationID]int
	traceIDs    []string
	onApply     func()
}

func newFakeTarget() *fakeTarget {
	return &fakeTarget{converged: map[string]bool{}, applyErrors: map[string]error{}, observeErrs: map[string]error{}, verifyErrs: map[string]error{}, applies: map[core.OperationID]int{}}
}

func (f *fakeTarget) Observe(ctx context.Context, resource domainmigration.Resource) (TargetObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.traceIDs = append(f.traceIDs, observability.TraceIDFromContext(ctx))
	return TargetObservation{Native: f.converged[resource.ID]}, f.observeErrs[resource.ID]
}

func (f *fakeTarget) Apply(ctx context.Context, step domainmigration.Step, resource domainmigration.Resource) error {
	f.mu.Lock()
	f.traceIDs = append(f.traceIDs, observability.TraceIDFromContext(ctx))
	f.applies[step.OperationID]++
	err := f.applyErrors[resource.ID]
	if err == nil || errors.Is(err, core.ErrOperationIndeterminate) {
		f.converged[resource.ID] = true
	}
	onApply := f.onApply
	f.mu.Unlock()
	if onApply != nil {
		onApply()
	}
	return err
}

func (f *fakeTarget) Verify(ctx context.Context, resource domainmigration.Resource, observation TargetObservation) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.traceIDs = append(f.traceIDs, observability.TraceIDFromContext(ctx))
	if err := f.verifyErrs[resource.ID]; err != nil {
		return false, err
	}
	value, _ := observation.Native.(bool)
	return value, nil
}

type evidenceSink struct {
	evidence []ExecutionEvidence
	err      error
}

func (s *evidenceSink) RecordExecution(_ context.Context, evidence ExecutionEvidence) error {
	if s.err != nil {
		return s.err
	}
	s.evidence = append(s.evidence, evidence)
	return nil
}

func executionPlan(t *testing.T, dependent bool) domainmigration.Plan {
	t.Helper()
	resources := []domainmigration.Resource{{ID: "a", Kind: "vm", Origin: domainmigration.ResourceOrigin{Source: "source", NativeType: "vm", NativeID: "a"}, Attributes: map[string]string{"owner": "host"}, Requirements: []domainmigration.Requirement{{Capability: "compute", Version: "v1"}}}}
	var edges []domainmigration.DependencyEdge
	if dependent {
		resources = append(resources, domainmigration.Resource{ID: "b", Kind: "vm", Origin: domainmigration.ResourceOrigin{Source: "source", NativeType: "vm", NativeID: "b"}, Attributes: map[string]string{"owner": "host"}, Requirements: []domainmigration.Requirement{{Capability: "compute", Version: "v1"}}})
		edges = []domainmigration.DependencyEdge{{From: "b", To: "a", Relation: "uses", Required: true}}
	}
	plan, err := domainmigration.BuildPlan(domainmigration.Aggregate{Discovery: domainmigration.DiscoveryComplete, Resources: resources, Edges: edges})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func tracedContext() context.Context {
	return observability.ContextWithTraceID(context.Background(), "trace-test")
}

func TestExecuteAlreadyConvergedDoesNotApply(t *testing.T) {
	target, sink := newFakeTarget(), &evidenceSink{}
	target.converged["a"] = true
	executor := NewExecutor(NewCapabilityResolver(executionCandidates{}), fakeFactory{operations: target}, sink)
	result, err := executor.Execute(tracedContext(), executionPlan(t, false))
	if err != nil || len(result.Evidence) != 1 || result.Evidence[0].Applied || result.Evidence[0].Status != StepConverged {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestExecuteAppliesOnceThenObservesAndVerifies(t *testing.T) {
	target, sink := newFakeTarget(), &evidenceSink{}
	plan := executionPlan(t, false)
	executor := NewExecutor(NewCapabilityResolver(executionCandidates{}), fakeFactory{operations: target}, sink)
	result, err := executor.Execute(tracedContext(), plan)
	if err != nil || target.applies[plan.Steps[0].OperationID] != 1 || len(target.traceIDs) != 5 {
		t.Fatalf("result=%+v applies=%v calls=%d err=%v", result, target.applies, len(target.traceIDs), err)
	}
	got := result.Evidence[0]
	if got.TraceID != "trace-test" || got.InputDigest != plan.InputDigest || got.PlanDigest != plan.Digest || got.CandidateDigest != testDigest('d') || !got.Applied || got.Status != StepConverged {
		t.Fatalf("evidence=%+v", got)
	}
	if got.ResolvedCapability != "compute" || got.VerifiedCapability != "compute" || !got.Verified || got.ObservationCount != 2 || got.VerificationCount != 2 {
		t.Fatalf("incomplete observed evidence=%+v", got)
	}
}

func TestExecuteIndeterminateObservesWithoutRetry(t *testing.T) {
	for _, tc := range []struct {
		name       string
		observable bool
		wantErr    bool
	}{{"converged", true, false}, {"not-observable", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			target, sink := newFakeTarget(), &evidenceSink{}
			target.applyErrors["a"] = core.ErrOperationIndeterminate
			if !tc.observable {
				target.observeErrs["a"] = errors.New("observation unavailable")
				// Permit the mandatory pre-observation; fail only after apply.
				calls := 0
				target.observeErrs = nil
				_ = calls
				target.onApply = func() {
					target.mu.Lock()
					target.observeErrs = map[string]error{"a": errors.New("observation unavailable")}
					target.mu.Unlock()
				}
			}
			plan := executionPlan(t, false)
			result, err := NewExecutor(NewCapabilityResolver(executionCandidates{}), fakeFactory{operations: target}, sink).Execute(tracedContext(), plan)
			if (err != nil) != tc.wantErr || target.applies[plan.Steps[0].OperationID] != 1 || !result.Evidence[0].Indeterminate {
				t.Fatalf("result=%+v applies=%v err=%v", result, target.applies, err)
			}
		})
	}
}

func TestExecuteFailureBlocksDependent(t *testing.T) {
	target, sink := newFakeTarget(), &evidenceSink{}
	target.applyErrors["a"] = errors.New("native rejection")
	plan := executionPlan(t, true)
	result, err := NewExecutor(NewCapabilityResolver(executionCandidates{}), fakeFactory{operations: target}, sink).Execute(tracedContext(), plan)
	if err == nil || len(result.Evidence) != 2 || result.Evidence[0].Status != StepFailed || result.Evidence[1].Status != StepBlocked || len(target.applies) != 1 {
		t.Fatalf("result=%+v applies=%v err=%v", result, target.applies, err)
	}
}

func TestExecuteVerifyFailureAndCancellation(t *testing.T) {
	t.Run("verify", func(t *testing.T) {
		target, sink := newFakeTarget(), &evidenceSink{}
		target.verifyErrs["a"] = errors.New("verification failure")
		result, err := NewExecutor(NewCapabilityResolver(executionCandidates{}), fakeFactory{operations: target}, sink).Execute(tracedContext(), executionPlan(t, false))
		if err == nil || result.Evidence[0].Status != StepFailed || len(target.applies) != 0 || result.Evidence[0].ObservationCount != 1 || result.Evidence[0].VerificationCount != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(tracedContext())
		target, sink := newFakeTarget(), &evidenceSink{}
		target.onApply = cancel
		_, err := NewExecutor(NewCapabilityResolver(executionCandidates{}), fakeFactory{operations: target}, sink).Execute(ctx, executionPlan(t, true))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestExecuteRejectsMissingDependenciesAndPropagatesSinkFailure(t *testing.T) {
	plan := executionPlan(t, false)
	executor := NewExecutor(NewCapabilityResolver(executionCandidates{}), fakeFactory{operations: newFakeTarget()}, nil)
	if _, err := executor.Execute(tracedContext(), plan); err == nil {
		t.Fatal("expected missing sink rejection")
	}
	if _, err := NewExecutor(NewCapabilityResolver(executionCandidates{}), fakeFactory{operations: newFakeTarget()}, &evidenceSink{err: errors.New("sink down")}).Execute(tracedContext(), plan); err == nil {
		t.Fatal("expected sink failure")
	}
	if _, err := NewExecutor(NewCapabilityResolver(executionCandidates{}), fakeFactory{operations: newFakeTarget()}, &evidenceSink{}).Execute(context.Background(), plan); err == nil {
		t.Fatal("expected missing trace rejection")
	}
}

func TestExecuteRepeatedPlanForwardsSameOperationID(t *testing.T) {
	target, sink := newFakeTarget(), &evidenceSink{}
	plan := executionPlan(t, false)
	executor := NewExecutor(NewCapabilityResolver(executionCandidates{}), fakeFactory{operations: target}, sink)
	if _, err := executor.Execute(tracedContext(), plan); err != nil {
		t.Fatal(err)
	}
	// Simulate an adapter journal replay: the already-converged observation means
	// the second execution does not invoke the mutation again.
	if _, err := executor.Execute(tracedContext(), plan); err != nil {
		t.Fatal(err)
	}
	if target.applies[plan.Steps[0].OperationID] != 1 {
		t.Fatalf("mutation repeated: %v", target.applies)
	}
}

type hostileMutatingTarget struct{ converged map[string]bool }

func mutateAdapterInputs(resource domainmigration.Resource) {
	if resource.Attributes != nil {
		resource.Attributes["owner"] = "attacker"
	}
	resource.Requirements[0].Capability = "attacker"
}

func (t *hostileMutatingTarget) Observe(_ context.Context, resource domainmigration.Resource) (TargetObservation, error) {
	mutateAdapterInputs(resource)
	return TargetObservation{Native: t.converged[resource.ID]}, nil
}

func (t *hostileMutatingTarget) Apply(_ context.Context, step domainmigration.Step, resource domainmigration.Resource) error {
	mutateAdapterInputs(resource)
	if len(step.DependsOn) > 0 {
		step.DependsOn[0] = "attacker"
	}
	t.converged[resource.ID] = true
	return nil
}

func (*hostileMutatingTarget) Verify(_ context.Context, resource domainmigration.Resource, observation TargetObservation) (bool, error) {
	mutateAdapterInputs(resource)
	converged, _ := observation.Native.(bool)
	return converged, nil
}

func TestExecuteDeepClonesPlanDataBeforeHostileAdapter(t *testing.T) {
	plan := executionPlan(t, true)
	wantDependency := plan.Steps[1].DependsOn[0]
	target := &hostileMutatingTarget{converged: map[string]bool{}}
	if _, err := NewExecutor(NewCapabilityResolver(executionCandidates{}), fakeFactory{operations: target}, &evidenceSink{}).Execute(tracedContext(), plan); err != nil {
		t.Fatal(err)
	}
	if got := plan.Resources[0].Attributes["owner"]; got != "host" {
		t.Fatalf("adapter mutated plan attributes: %q", got)
	}
	if got := plan.Resources[0].Requirements[0].Capability; got != "compute" {
		t.Fatalf("adapter mutated plan requirements: %q", got)
	}
	if got := plan.Steps[1].DependsOn[0]; got != wantDependency {
		t.Fatalf("adapter mutated plan dependencies: %q", got)
	}
}

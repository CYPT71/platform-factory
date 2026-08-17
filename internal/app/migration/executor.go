package migration

import (
	"context"
	"errors"
	"fmt"

	"github.com/CYPT71/platform-factory/internal/core"
	domainmigration "github.com/CYPT71/platform-factory/internal/migration"
	"github.com/CYPT71/platform-factory/internal/observability"
)

// TargetOperations is owned by the migration use case. Infrastructure adapters
// implement it without exposing provider-native state to the domain.
type TargetOperations interface {
	Observe(context.Context, domainmigration.Resource) (TargetObservation, error)
	Apply(context.Context, domainmigration.Step, domainmigration.Resource) error
	Verify(context.Context, domainmigration.Resource, TargetObservation) (bool, error)
}

// TargetOperationsFactory binds operations to the exact verified implementation
// selected by capability resolution.
type TargetOperationsFactory interface {
	Open(context.Context, ResolvedCapability) (TargetOperations, error)
}

// TargetObservation is deliberately opaque: only its adapter may interpret the
// native state, while the host controls sequencing and convergence decisions.
type TargetObservation struct{ Native any }

// ExecutionEvidence contains identifiers and observed outcomes only. It never
// stores native observations or raw errors, which may carry secrets.
type ExecutionEvidence struct {
	TraceID            string
	InputDigest        string
	PlanDigest         string
	OperationID        core.OperationID
	ResourceID         string
	Capability         string // requested capability
	CapabilityVersion  string
	CandidateID        string
	CandidateDigest    string
	ResolvedCapability string
	VerifiedCapability string
	Compatibility      Compatibility
	Gaps               []domainmigration.CompatibilityGap
	ObservationCount   uint32
	VerificationCount  uint32
	Verified           bool
	Status             StepStatus
	Applied            bool
	Indeterminate      bool
}

type StepStatus string

const (
	StepConverged StepStatus = "converged"
	StepFailed    StepStatus = "failed"
	StepBlocked   StepStatus = "blocked"
)

type ProvenanceSink interface {
	RecordExecution(context.Context, ExecutionEvidence) error
}

type ExecutionResult struct{ Evidence []ExecutionEvidence }

type Executor struct {
	resolver *CapabilityResolver
	factory  TargetOperationsFactory
	sink     ProvenanceSink
}

func NewExecutor(resolver *CapabilityResolver, factory TargetOperationsFactory, sink ProvenanceSink) *Executor {
	return &Executor{resolver: resolver, factory: factory, sink: sink}
}

// Execute preserves plan order, never retries a mutation, and proves convergence
// by observation. An indeterminate mutation is observed, never blindly repeated.
func (e *Executor) Execute(ctx context.Context, plan domainmigration.Plan) (ExecutionResult, error) {
	if err := ctx.Err(); err != nil {
		return ExecutionResult{}, err
	}
	traceID := observability.TraceIDFromContext(ctx)
	if traceID == "" {
		return ExecutionResult{}, errors.New("migration execute: trace ID is required")
	}
	if e == nil || e.resolver == nil || e.factory == nil || e.sink == nil {
		return ExecutionResult{}, errors.New("migration execute: resolver, operations factory, and provenance sink are required")
	}
	if err := plan.Validate(); err != nil {
		return ExecutionResult{}, fmt.Errorf("migration execute: validate plan: %w", err)
	}

	resources := make(map[string]domainmigration.Resource, len(plan.Resources))
	for _, resource := range plan.Resources {
		resources[resource.ID] = resource
	}
	status := make(map[core.OperationID]StepStatus, len(plan.Steps))
	result := ExecutionResult{Evidence: make([]ExecutionEvidence, 0, len(plan.Steps))}
	var executionErr error
	for _, step := range plan.Steps {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(executionErr, err)
		}
		evidence := newExecutionEvidence(traceID, plan, step)
		if dependenciesFailed(step.DependsOn, status) {
			evidence.Status = StepBlocked
			status[step.OperationID] = StepBlocked
			if err := e.record(ctx, &result, evidence); err != nil {
				return result, errors.Join(executionErr, err)
			}
			continue
		}

		resolved, err := e.resolver.Resolve(ctx, CapabilityRequirement{Capability: step.Capability, Version: step.Version})
		if err == nil {
			evidence.CandidateID, evidence.CandidateDigest = resolved.CandidateID, resolved.Digest
			evidence.ResolvedCapability = step.Capability
			evidence.VerifiedCapability = step.Capability
			evidence.Compatibility = resolved.Compatibility
		}
		var operations TargetOperations
		if err == nil {
			operations, err = e.factory.Open(ctx, resolved)
			if err == nil && operations == nil {
				err = errors.New("target operations factory returned nil")
			}
		}
		resource := resources[step.ResourceID]
		var observed TargetObservation
		if err == nil {
			observed, err = operations.Observe(ctx, cloneResource(resource))
			if err == nil {
				evidence.ObservationCount++
			}
		}
		var converged bool
		if err == nil {
			converged, err = operations.Verify(ctx, cloneResource(resource), observed)
			if err == nil {
				evidence.VerificationCount++
			}
			evidence.Verified = err == nil && converged
		}
		if err == nil && !converged {
			evidence.Applied = true
			applyErr := operations.Apply(ctx, cloneStep(step), cloneResource(resource))
			evidence.Indeterminate = errors.Is(applyErr, core.ErrOperationIndeterminate)
			// Observation is mandatory after every attempted mutation, including
			// rejected and indeterminate results. The mutation is never retried.
			observed, observeErr := operations.Observe(ctx, cloneResource(resource))
			if observeErr == nil {
				evidence.ObservationCount++
			}
			var verifyErr error
			if observeErr == nil {
				converged, verifyErr = operations.Verify(ctx, cloneResource(resource), observed)
				if verifyErr == nil {
					evidence.VerificationCount++
				}
				evidence.Verified = verifyErr == nil && converged
			}
			if converged && verifyErr == nil {
				// Observed convergence is stronger evidence than command transport
				// success: a provider error may follow a completed mutation.
				err = nil
			} else {
				err = errors.Join(applyErr, observeErr, verifyErr)
				if err == nil {
					err = errors.New("target did not converge after apply")
				}
			}
		}
		if err != nil {
			evidence.Status = StepFailed
			executionErr = errors.Join(executionErr, fmt.Errorf("migration operation %q failed: %w", step.OperationID, err))
		} else {
			evidence.Status = StepConverged
		}
		status[step.OperationID] = evidence.Status
		if err := e.record(ctx, &result, evidence); err != nil {
			return result, errors.Join(executionErr, err)
		}
	}
	return result, executionErr
}

func newExecutionEvidence(traceID string, plan domainmigration.Plan, step domainmigration.Step) ExecutionEvidence {
	gaps := make([]domainmigration.CompatibilityGap, 0)
	for _, gap := range plan.Gaps {
		if gap.ResourceID == step.ResourceID {
			gaps = append(gaps, gap)
		}
	}
	return ExecutionEvidence{
		TraceID: traceID, InputDigest: plan.InputDigest, PlanDigest: plan.Digest,
		OperationID: step.OperationID, ResourceID: step.ResourceID,
		Capability: step.Capability, CapabilityVersion: step.Version, Gaps: gaps,
	}
}

func cloneResource(resource domainmigration.Resource) domainmigration.Resource {
	cloned := resource
	cloned.Attributes = make(map[string]string, len(resource.Attributes))
	for key, value := range resource.Attributes {
		cloned.Attributes[key] = value
	}
	cloned.Requirements = append([]domainmigration.Requirement(nil), resource.Requirements...)
	return cloned
}

func cloneStep(step domainmigration.Step) domainmigration.Step {
	cloned := step
	cloned.DependsOn = append([]core.OperationID(nil), step.DependsOn...)
	return cloned
}

func dependenciesFailed(dependencies []core.OperationID, statuses map[core.OperationID]StepStatus) bool {
	for _, dependency := range dependencies {
		if statuses[dependency] != StepConverged {
			return true
		}
	}
	return false
}

func (e *Executor) record(ctx context.Context, result *ExecutionResult, evidence ExecutionEvidence) error {
	if err := e.sink.RecordExecution(ctx, evidence); err != nil {
		return fmt.Errorf("migration execute: record evidence for %q: %w", evidence.OperationID, err)
	}
	result.Evidence = append(result.Evidence, evidence)
	return nil
}

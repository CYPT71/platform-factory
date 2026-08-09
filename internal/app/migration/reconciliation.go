package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/CYPT71/secure-oci-base/internal/core"
	domainmigration "github.com/CYPT71/secure-oci-base/internal/migration"
	"github.com/CYPT71/secure-oci-base/internal/observability"
)

const DefaultReconciliationIterations = 8
const maxDifferenceFingerprintBytes = 1024

var (
	ErrReconciliationNoProgress  = errors.New("migration reconciliation made no progress")
	ErrReconciliationOscillation = errors.New("migration reconciliation oscillated")
	ErrReconciliationLimit       = errors.New("migration reconciliation iteration limit reached")
	ErrReconciliationUnknown     = errors.New("migration reconciliation difference is unknown")
)

// TargetDifference is an adapter-normalized measure of the remaining work.
// Distance must strictly decrease after an action. Fingerprint identifies the
// logical difference without retaining provider-native data or secrets.
type TargetDifference struct {
	Known       bool
	Distance    uint64
	Fingerprint string
}

// ProgressOperations extends TargetOperations only for iterative
// reconciliation. Existing one-shot executors and adapters remain compatible.
type ProgressOperations interface {
	TargetOperations
	Difference(context.Context, domainmigration.Resource, TargetObservation) (TargetDifference, error)
}

type ReconciliationOptions struct{ MaxIterations int }

// Reconcile drives every plan step through observe, diff, action, observe until
// convergence. It uses the Executor's resolver, factory, evidence sink, and
// operation contract rather than introducing a second orchestration pipeline.
func (e *Executor) Reconcile(ctx context.Context, plan domainmigration.Plan, options ReconciliationOptions) (ExecutionResult, error) {
	if err := ctx.Err(); err != nil {
		return ExecutionResult{}, err
	}
	traceID := observability.TraceIDFromContext(ctx)
	if traceID == "" {
		return ExecutionResult{}, errors.New("migration reconcile: trace ID is required")
	}
	if e == nil || e.resolver == nil || e.factory == nil || e.sink == nil {
		return ExecutionResult{}, errors.New("migration reconcile: resolver, operations factory, and provenance sink are required")
	}
	if err := plan.Validate(); err != nil {
		return ExecutionResult{}, fmt.Errorf("migration reconcile: validate plan: %w", err)
	}
	maxIterations := options.MaxIterations
	if maxIterations == 0 {
		maxIterations = DefaultReconciliationIterations
	}
	if maxIterations < 1 {
		return ExecutionResult{}, errors.New("migration reconcile: max iterations must be positive")
	}

	resources := make(map[string]domainmigration.Resource, len(plan.Resources))
	for _, resource := range plan.Resources {
		resources[resource.ID] = resource
	}
	statuses := make(map[core.OperationID]StepStatus, len(plan.Steps))
	result := ExecutionResult{Evidence: make([]ExecutionEvidence, 0, len(plan.Steps))}
	var reconciliationErr error
	for _, step := range plan.Steps {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(reconciliationErr, err)
		}
		evidence := newExecutionEvidence(traceID, plan, step)
		if dependenciesFailed(step.DependsOn, statuses) {
			evidence.Status = StepBlocked
			statuses[step.OperationID] = StepBlocked
			if err := e.record(ctx, &result, evidence); err != nil {
				return result, errors.Join(reconciliationErr, err)
			}
			continue
		}

		err := e.reconcileStep(ctx, resources[step.ResourceID], step, maxIterations, &evidence)
		if err != nil {
			evidence.Status = StepFailed
			reconciliationErr = errors.Join(reconciliationErr, fmt.Errorf("migration operation %q failed: %w", step.OperationID, err))
		} else {
			evidence.Status = StepConverged
		}
		statuses[step.OperationID] = evidence.Status
		if err := e.record(ctx, &result, evidence); err != nil {
			return result, errors.Join(reconciliationErr, err)
		}
	}
	return result, reconciliationErr
}

func (e *Executor) reconcileStep(ctx context.Context, resource domainmigration.Resource, step domainmigration.Step, maxIterations int, evidence *ExecutionEvidence) error {
	resolved, err := e.resolver.Resolve(ctx, CapabilityRequirement{Capability: step.Capability, Version: step.Version})
	if err != nil {
		return err
	}
	evidence.CandidateID, evidence.CandidateDigest = resolved.CandidateID, resolved.Digest
	evidence.ResolvedCapability = step.Capability
	evidence.VerifiedCapability = step.Capability
	evidence.Compatibility = resolved.Compatibility
	operations, err := e.factory.Open(ctx, resolved)
	if err != nil {
		return err
	}
	progress, ok := operations.(ProgressOperations)
	if !ok || progress == nil {
		return errors.New("target operations do not provide reconciliation differences")
	}

	seen := make(map[string]struct{}, maxIterations)
	for range maxIterations {
		if err := ctx.Err(); err != nil {
			return err
		}
		observed, err := progress.Observe(ctx, cloneResource(resource))
		if err == nil {
			evidence.ObservationCount++
		}
		if err != nil {
			return fmt.Errorf("observe target: %w", err)
		}
		converged, err := progress.Verify(ctx, cloneResource(resource), observed)
		if err == nil {
			evidence.VerificationCount++
		}
		evidence.Verified = err == nil && converged
		if err != nil {
			return fmt.Errorf("verify target: %w", err)
		}
		if converged {
			return nil
		}
		before, err := progress.Difference(ctx, cloneResource(resource), observed)
		if err != nil {
			return fmt.Errorf("measure target difference: %w", err)
		}
		if err := validateDifference(before); err != nil {
			return err
		}
		if _, duplicate := seen[before.Fingerprint]; duplicate {
			return ErrReconciliationOscillation
		}
		seen[before.Fingerprint] = struct{}{}

		action := cloneStep(step)
		action.OperationID = reconciliationOperationID(step.OperationID, before)
		evidence.Applied = true
		applyErr := progress.Apply(ctx, action, cloneResource(resource))
		if errors.Is(applyErr, core.ErrOperationIndeterminate) {
			evidence.Indeterminate = true
		}
		observed, observeErr := progress.Observe(ctx, cloneResource(resource))
		if observeErr == nil {
			evidence.ObservationCount++
		}
		if observeErr != nil {
			return errors.Join(applyErr, fmt.Errorf("observe after action: %w", observeErr))
		}
		converged, verifyErr := progress.Verify(ctx, cloneResource(resource), observed)
		if verifyErr == nil {
			evidence.VerificationCount++
		}
		evidence.Verified = verifyErr == nil && converged
		if verifyErr != nil {
			return errors.Join(applyErr, fmt.Errorf("verify after action: %w", verifyErr))
		}
		if converged {
			return nil
		}
		after, differenceErr := progress.Difference(ctx, cloneResource(resource), observed)
		if differenceErr != nil {
			return errors.Join(applyErr, fmt.Errorf("measure difference after action: %w", differenceErr))
		}
		if err := validateDifference(after); err != nil {
			return errors.Join(applyErr, err)
		}
		if after.Distance >= before.Distance {
			return errors.Join(applyErr, fmt.Errorf("%w: distance %d became %d", ErrReconciliationNoProgress, before.Distance, after.Distance))
		}
		if applyErr != nil && !errors.Is(applyErr, core.ErrOperationIndeterminate) {
			return applyErr
		}
	}
	return ErrReconciliationLimit
}

func validateDifference(difference TargetDifference) error {
	if !difference.Known || difference.Fingerprint == "" || len(difference.Fingerprint) > maxDifferenceFingerprintBytes {
		return ErrReconciliationUnknown
	}
	if difference.Distance == 0 {
		return fmt.Errorf("%w: zero distance did not verify as converged", ErrReconciliationUnknown)
	}
	return nil
}

func reconciliationOperationID(base core.OperationID, difference TargetDifference) core.OperationID {
	sum := sha256.Sum256([]byte(fmt.Sprintf("platform-factory/migration-reconciliation-operation/v1\x00%s\x00%d\x00%s", base, difference.Distance, difference.Fingerprint)))
	return core.OperationID("migration-" + hex.EncodeToString(sum[:]))
}

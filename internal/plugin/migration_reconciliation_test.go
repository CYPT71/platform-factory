package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	appmigration "github.com/CYPT71/platform-factory/internal/app/migration"
	domainmigration "github.com/CYPT71/platform-factory/internal/migration"
	"github.com/CYPT71/platform-factory/internal/observability"
)

type migrationEvidenceSink struct {
	evidence []appmigration.ExecutionEvidence
}

func (s *migrationEvidenceSink) RecordExecution(_ context.Context, evidence appmigration.ExecutionEvidence) error {
	s.evidence = append(s.evidence, evidence)
	return nil
}

func migrationApplyPlan(t *testing.T, resource domainmigration.Resource) domainmigration.Plan {
	t.Helper()
	plan, err := domainmigration.BuildPlan(domainmigration.Aggregate{
		Discovery: domainmigration.DiscoveryComplete,
		Resources: []domainmigration.Resource{resource},
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestMigrationTargetDifferenceIsHostOwnedDeterministicAndOpaque(t *testing.T) {
	desired := migrationResource()
	desired.Attributes["description"] = "sensitive-sentinel-value"
	desired.Requirements = append(desired.Requirements, domainmigration.Requirement{Capability: "network", Version: "v2"})
	observed := desired
	observed.Attributes = map[string]string{"description": "different", "cpu": "1", "extra": "present"}
	observed.Requirements = []domainmigration.Requirement{{Capability: "network", Version: "v2"}}
	target := &migrationTarget{}

	first, err := target.Difference(context.Background(), desired, appmigration.TargetObservation{Native: normalizedObservation{found: true, resource: observed}})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Known || first.Distance != 4 || !strings.HasPrefix(first.Fingerprint, "sha256:") || strings.Contains(first.Fingerprint, "sensitive-sentinel-value") {
		t.Fatalf("difference=%+v", first)
	}

	// Map insertion and requirement order cannot alter the host fingerprint.
	desired.Attributes = map[string]string{"description": "sensitive-sentinel-value", "cpu": "2"}
	desired.Requirements[0], desired.Requirements[1] = desired.Requirements[1], desired.Requirements[0]
	second, err := target.Difference(context.Background(), desired, appmigration.TargetObservation{Native: normalizedObservation{found: true, resource: observed}})
	if err != nil || second != first {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}

	absent, err := target.Difference(context.Background(), desired, appmigration.TargetObservation{Native: normalizedObservation{}})
	if err != nil || absent.Distance != 9 || absent.Fingerprint == first.Fingerprint {
		t.Fatalf("absent=%+v err=%v", absent, err)
	}
	if _, err := target.Difference(context.Background(), desired, appmigration.TargetObservation{Native: "plugin-claimed-converged"}); err == nil {
		t.Fatal("untrusted observation shape accepted")
	}
}

func TestMigrationTargetNormalizesNilAndEmptyCollectionsAcrossWire(t *testing.T) {
	desired := domainmigration.Resource{
		ID: "empty-1", Kind: "compute",
		Origin: domainmigration.ResourceOrigin{Source: "source", NativeType: "vm", NativeID: "empty"},
	}
	wire := resourceToWire(desired)
	if wire.Attributes != nil || wire.Requirements != nil {
		t.Fatalf("wire manufactured empty collections: %+v", wire)
	}
	// JSON decoders may still produce empty non-nil collections. They are
	// semantically identical and must not lead Verify and Difference to disagree.
	wire.Attributes = map[string]string{}
	wire.Requirements = []migrationWireRequirement{}
	observed := resourceFromWire(wire)
	observation := appmigration.TargetObservation{Native: normalizedObservation{found: true, resource: observed}}
	target := &migrationTarget{}
	verified, err := target.Verify(context.Background(), desired, observation)
	if err != nil || !verified {
		t.Fatalf("verified=%v err=%v desired=%+v observed=%+v", verified, err, desired, observed)
	}
	difference, err := target.Difference(context.Background(), desired, observation)
	if err != nil || !difference.Known || difference.Distance != 0 || difference.Fingerprint == "" {
		t.Fatalf("difference=%+v err=%v", difference, err)
	}
}

func TestMigrationExternalPluginReconcileDivergenceToConvergence(t *testing.T) {
	desired := migrationResource()
	state := desired
	state.Attributes = map[string]string{"cpu": "1"}
	applyCalls := 0
	var operationID string
	r, _ := migrationTestRegistry(t, map[string]testHandler{
		migrationObserveCapability: func(context.Context, json.RawMessage) (any, error) {
			wire := resourceToWire(state)
			return migrationObserveResult{Found: true, Resource: &wire}, nil
		},
		migrationApplyCapability: func(_ context.Context, raw json.RawMessage) (any, error) {
			var params migrationApplyParams
			if err := json.Unmarshal(raw, &params); err != nil {
				return nil, err
			}
			applyCalls++
			operationID = params.Step.OperationID
			state = resourceFromWire(params.Resource)
			return migrationApplyResult{Accepted: true}, nil
		},
	})
	sink := &migrationEvidenceSink{}
	executor := appmigration.NewExecutor(appmigration.NewCapabilityResolver(r), MigrationTargetFactory{Registry: r}, sink)
	ctx := observability.ContextWithTraceID(context.Background(), "plugin-reconcile")
	plan := migrationApplyPlan(t, desired)
	result, err := executor.Reconcile(ctx, plan, appmigration.ReconciliationOptions{MaxIterations: 2})
	if err != nil || applyCalls != 1 || len(result.Evidence) != 1 || result.Evidence[0].Status != appmigration.StepConverged {
		t.Fatalf("result=%+v calls=%d err=%v", result, applyCalls, err)
	}
	if operationID == "" || operationID == string(plan.Steps[0].OperationID) {
		t.Fatalf("reconciliation operation ID was not derived from observed difference: %q", operationID)
	}
}

func TestMigrationExternalPluginReconcileNoProgressAndJournalReplay(t *testing.T) {
	desired := migrationResource()
	state := desired
	state.Attributes = map[string]string{"cpu": "1"}
	applyCalls := 0
	var firstOperationID string
	r, _ := migrationTestRegistry(t, map[string]testHandler{
		migrationObserveCapability: func(context.Context, json.RawMessage) (any, error) {
			wire := resourceToWire(state)
			return migrationObserveResult{Found: true, Resource: &wire}, nil
		},
		migrationApplyCapability: func(_ context.Context, raw json.RawMessage) (any, error) {
			var params migrationApplyParams
			if err := json.Unmarshal(raw, &params); err != nil {
				return nil, err
			}
			applyCalls++
			firstOperationID = params.Step.OperationID
			// Accepted command without observed state change is not success.
			return migrationApplyResult{Accepted: true}, nil
		},
	})
	executor := appmigration.NewExecutor(appmigration.NewCapabilityResolver(r), MigrationTargetFactory{Registry: r}, &migrationEvidenceSink{})
	ctx := observability.ContextWithTraceID(context.Background(), "plugin-no-progress")
	plan := migrationApplyPlan(t, desired)
	if _, err := executor.Reconcile(ctx, plan, appmigration.ReconciliationOptions{MaxIterations: 2}); !errors.Is(err, appmigration.ErrReconciliationNoProgress) {
		t.Fatalf("first reconcile err=%v", err)
	}
	if firstOperationID == "" || applyCalls != 1 {
		t.Fatalf("operation=%q calls=%d", firstOperationID, applyCalls)
	}

	// The same canonical difference derives the same OperationID. The existing
	// journal intercepts the replay before it can cause a second plugin effect.
	if _, err := executor.Reconcile(ctx, plan, appmigration.ReconciliationOptions{MaxIterations: 2}); !errors.Is(err, appmigration.ErrReconciliationNoProgress) || !strings.Contains(err.Error(), "replayable result") {
		t.Fatalf("replay err=%v", err)
	}
	if applyCalls != 1 {
		t.Fatalf("journal replay reached plugin: calls=%d", applyCalls)
	}
}

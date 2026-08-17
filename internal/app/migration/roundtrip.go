package migration

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	domainmigration "github.com/CYPT71/platform-factory/internal/migration"
	"github.com/CYPT71/platform-factory/internal/observability"
)

// RoundTripResult binds every host-controlled stage needed to prove that a
// canonical source graph was materialized without semantic loss. Provider
// native observations remain outside this result and are never trusted as
// verification by themselves.
type RoundTripResult struct {
	Source    domainmigration.Aggregate
	Plan      domainmigration.Plan
	Execution ExecutionResult
	Target    domainmigration.Aggregate
}

type DiscoveryIdentity struct {
	PluginID   string
	Digest     string
	Capability string
}

type VerifiedDiscoverySource interface {
	DiscoverySource
	VerifiedDiscoveryIdentity(context.Context) (DiscoveryIdentity, error)
}

type WorkflowEvidence struct {
	TraceID               string
	SourcePluginID        string
	SourcePluginDigest    string
	SourceCapability      string
	CanonicalGraphDigest  string
	PlanDigest            string
	SourceResourceIDs     []string
	TargetResourceIDs     []string
	TargetPluginIDs       []string
	TargetPluginDigests   []string
	RequestedCapabilities []string
	ResolvedCapabilities  []string
	VerifiedCapabilities  []string
	OperationIDs          []string
	CompatibilityGaps     []domainmigration.CompatibilityGap
	ExternalDependencies  []domainmigration.ExternalDependency
	Transformations       []domainmigration.Transformation
	ObservationCount      uint32
	VerificationCount     uint32
	FinalState            string
}

type WorkflowProvenanceSink interface {
	RecordWorkflow(context.Context, WorkflowEvidence) error
}

// RoundTripValidator composes the existing discovery, planning and execution
// use cases. It does not let either plugin compare or approve its own output.
type RoundTripValidator struct {
	discoverer *Discoverer
	planner    *Planner
	executor   *Executor
	provenance WorkflowProvenanceSink
}

func NewRoundTripValidatorWithProvenance(discoverer *Discoverer, planner *Planner, executor *Executor, provenance WorkflowProvenanceSink) *RoundTripValidator {
	return &RoundTripValidator{discoverer: discoverer, planner: planner, executor: executor, provenance: provenance}
}

func NewRoundTripValidator(discoverer *Discoverer, planner *Planner, executor *Executor) *RoundTripValidator {
	return &RoundTripValidator{discoverer: discoverer, planner: planner, executor: executor}
}

// Validate discovers a source, materializes its deterministic plan through a
// verified target, rediscovers the target, and compares both canonical graphs
// host-side. Origins are intentionally excluded from semantic equality because
// provider and native IDs necessarily change across a migration boundary.
func (v *RoundTripValidator) Validate(ctx context.Context, source, target DiscoverySource) (RoundTripResult, error) {
	if err := ctx.Err(); err != nil {
		return RoundTripResult{}, err
	}
	if v == nil || v.discoverer == nil || v.planner == nil || v.executor == nil {
		return RoundTripResult{}, errors.New("migration round-trip: discoverer, planner, and executor are required")
	}
	if source == nil || target == nil {
		return RoundTripResult{}, errors.New("migration round-trip: source and target discovery are required")
	}
	result := RoundTripResult{}
	var err error
	var sourceIdentity DiscoveryIdentity
	if v.provenance != nil {
		verified, ok := source.(VerifiedDiscoverySource)
		if !ok {
			return result, errors.New("migration round-trip: workflow provenance requires a verified source identity")
		}
		sourceIdentity, err = verified.VerifiedDiscoveryIdentity(ctx)
		if err != nil {
			return result, fmt.Errorf("migration round-trip: verify source identity: %w", err)
		}
		if sourceIdentity.PluginID == "" || sourceIdentity.Digest == "" || sourceIdentity.Capability == "" {
			return result, errors.New("migration round-trip: verified source identity is incomplete")
		}
	}

	result.Source, err = v.discoverer.Discover(ctx, source)
	if err != nil {
		return result, fmt.Errorf("migration round-trip: discover source: %w", err)
	}
	if result.Source.Discovery != domainmigration.DiscoveryComplete {
		return result, errors.New("migration round-trip: source discovery must be complete")
	}
	result.Plan, err = v.planner.Build(ctx, result.Source)
	if err != nil {
		return result, fmt.Errorf("migration round-trip: build plan: %w", err)
	}
	result.Execution, err = v.executor.Execute(ctx, result.Plan)
	if err != nil {
		return result, fmt.Errorf("migration round-trip: execute plan: %w", err)
	}
	result.Target, err = v.discoverer.Discover(ctx, target)
	if err != nil {
		return result, fmt.Errorf("migration round-trip: discover target: %w", err)
	}
	if err := ValidateSemanticRoundTrip(result.Source, result.Target); err != nil {
		return result, fmt.Errorf("migration round-trip: %w", err)
	}
	if v.provenance != nil {
		evidence := buildWorkflowEvidence(ctx, sourceIdentity, result)
		if err := v.provenance.RecordWorkflow(ctx, evidence); err != nil {
			return result, fmt.Errorf("migration round-trip: record workflow provenance: %w", err)
		}
	}
	return result, nil
}

func buildWorkflowEvidence(ctx context.Context, source DiscoveryIdentity, result RoundTripResult) WorkflowEvidence {
	evidence := WorkflowEvidence{TraceID: observability.TraceIDFromContext(ctx), SourcePluginID: source.PluginID, SourcePluginDigest: source.Digest, SourceCapability: source.Capability, CanonicalGraphDigest: result.Plan.InputDigest, PlanDigest: result.Plan.Digest, FinalState: "verified"}
	evidence.ExternalDependencies = append([]domainmigration.ExternalDependency(nil), result.Plan.ExternalDependencies...)
	evidence.Transformations = append([]domainmigration.Transformation(nil), result.Plan.Transformations...)
	for _, resource := range result.Source.Resources {
		evidence.SourceResourceIDs = append(evidence.SourceResourceIDs, resource.ID)
	}
	for _, resource := range result.Target.Resources {
		evidence.TargetResourceIDs = append(evidence.TargetResourceIDs, resource.ID)
	}
	for _, step := range result.Plan.Steps {
		evidence.OperationIDs = append(evidence.OperationIDs, string(step.OperationID))
	}
	for _, item := range result.Execution.Evidence {
		evidence.TargetPluginIDs = appendUnique(evidence.TargetPluginIDs, item.CandidateID)
		evidence.TargetPluginDigests = appendUnique(evidence.TargetPluginDigests, item.CandidateDigest)
		evidence.RequestedCapabilities = appendUnique(evidence.RequestedCapabilities, item.Capability)
		evidence.ResolvedCapabilities = appendUnique(evidence.ResolvedCapabilities, item.ResolvedCapability)
		evidence.VerifiedCapabilities = appendUnique(evidence.VerifiedCapabilities, item.VerifiedCapability)
		evidence.CompatibilityGaps = append(evidence.CompatibilityGaps, item.Gaps...)
		evidence.ObservationCount += item.ObservationCount
		evidence.VerificationCount += item.VerificationCount
		if item.Status != StepConverged || !item.Verified {
			evidence.FinalState = "failed"
		}
	}
	sort.Strings(evidence.SourceResourceIDs)
	sort.Strings(evidence.TargetResourceIDs)
	sort.Strings(evidence.TargetPluginIDs)
	sort.Strings(evidence.TargetPluginDigests)
	sort.Strings(evidence.RequestedCapabilities)
	sort.Strings(evidence.ResolvedCapabilities)
	sort.Strings(evidence.VerifiedCapabilities)
	sort.Strings(evidence.OperationIDs)
	return evidence
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// ValidateSemanticRoundTrip compares only host-owned canonical semantics. It
// fails closed on incomplete discovery, unknowns, gaps, missing resources,
// additional resources, changed requirements, attributes, or graph edges.
func ValidateSemanticRoundTrip(source, target domainmigration.Aggregate) error {
	if err := source.Validate(); err != nil {
		return fmt.Errorf("invalid source aggregate: %w", err)
	}
	if err := target.Validate(); err != nil {
		return fmt.Errorf("invalid target aggregate: %w", err)
	}
	if source.Discovery != domainmigration.DiscoveryComplete || target.Discovery != domainmigration.DiscoveryComplete {
		return errors.New("source and target discovery must both be complete")
	}
	want := semanticAggregate(source.Canonical())
	got := semanticAggregate(target.Canonical())
	if !reflect.DeepEqual(want, got) {
		return errors.New("target canonical graph differs from source semantics")
	}
	return nil
}

type semanticResource struct {
	ID           string
	Kind         string
	Attributes   map[string]string
	Requirements []domainmigration.Requirement
}

type semanticState struct {
	Resources            []semanticResource
	Edges                []domainmigration.DependencyEdge
	ExternalDependencies []domainmigration.ExternalDependency
	Transformations      []domainmigration.Transformation
}

func semanticAggregate(aggregate domainmigration.Aggregate) semanticState {
	state := semanticState{Resources: make([]semanticResource, len(aggregate.Resources)), Edges: append([]domainmigration.DependencyEdge(nil), aggregate.Edges...), ExternalDependencies: append([]domainmigration.ExternalDependency(nil), aggregate.ExternalDependencies...), Transformations: append([]domainmigration.Transformation(nil), aggregate.Transformations...)}
	for i, resource := range aggregate.Resources {
		attributes := make(map[string]string, len(resource.Attributes))
		for key, value := range resource.Attributes {
			attributes[key] = value
		}
		state.Resources[i] = semanticResource{ID: resource.ID, Kind: resource.Kind, Attributes: attributes, Requirements: append([]domainmigration.Requirement(nil), resource.Requirements...)}
	}
	return state
}

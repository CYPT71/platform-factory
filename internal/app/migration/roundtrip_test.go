package migration

import (
	"context"
	"strings"
	"testing"

	domainmigration "github.com/CYPT71/platform-factory/internal/migration"
	"github.com/CYPT71/platform-factory/internal/observability"
)

type roundTripEnvironment struct {
	sourceID  string
	resources map[string]domainmigration.Resource
	edges     []domainmigration.DependencyEdge
}

func (e *roundTripEnvironment) VerifiedDiscoveryIdentity(context.Context) (DiscoveryIdentity, error) {
	return DiscoveryIdentity{PluginID: e.sourceID, Digest: testDigest('a'), Capability: "migration.discover"}, nil
}

type workflowSink struct{ records []WorkflowEvidence }

func (s *workflowSink) RecordWorkflow(_ context.Context, evidence WorkflowEvidence) error {
	s.records = append(s.records, evidence)
	return nil
}

func (e *roundTripEnvironment) SourceID() string { return e.sourceID }

func (e *roundTripEnvironment) DiscoverPage(context.Context, string) (DiscoveryPage, error) {
	resources := make([]domainmigration.Resource, 0, len(e.resources))
	for _, resource := range e.resources {
		copy := cloneResource(resource)
		copy.Origin.Source = e.sourceID
		copy.Origin.NativeType = "native-" + copy.Kind
		copy.Origin.NativeID = e.sourceID + "/" + copy.ID
		resources = append(resources, copy)
	}
	return DiscoveryPage{Status: domainmigration.DiscoveryComplete, Resources: resources, Edges: append([]domainmigration.DependencyEdge(nil), e.edges...)}, nil
}

type roundTripTarget struct{ environment *roundTripEnvironment }

func (t roundTripTarget) Observe(_ context.Context, resource domainmigration.Resource) (TargetObservation, error) {
	_, found := t.environment.resources[resource.ID]
	return TargetObservation{Native: found}, nil
}

func (t roundTripTarget) Apply(_ context.Context, _ domainmigration.Step, resource domainmigration.Resource) error {
	t.environment.resources[resource.ID] = cloneResource(resource)
	_, hasAPI := t.environment.resources["api"]
	_, hasDatabase := t.environment.resources["database"]
	if hasAPI && hasDatabase {
		t.environment.edges = []domainmigration.DependencyEdge{{From: "api", To: "database", Relation: "uses", Required: true}}
	}
	return nil
}

func (t roundTripTarget) Verify(_ context.Context, _ domainmigration.Resource, observation TargetObservation) (bool, error) {
	found, _ := observation.Native.(bool)
	return found, nil
}

func roundTripResource(id string) domainmigration.Resource {
	return domainmigration.Resource{ID: id, Kind: "service", Origin: domainmigration.ResourceOrigin{Source: "source", NativeType: "unit", NativeID: id}, Attributes: map[string]string{"tier": id}, Requirements: []domainmigration.Requirement{{Capability: "compute", Version: "v1"}}}
}

func TestRoundTripValidatorDiscoversPlansAppliesRediscoversAndVerifies(t *testing.T) {
	source := &roundTripEnvironment{sourceID: "source-plugin", resources: map[string]domainmigration.Resource{"database": roundTripResource("database"), "api": roundTripResource("api")}, edges: []domainmigration.DependencyEdge{{From: "api", To: "database", Relation: "uses", Required: true}}}
	target := &roundTripEnvironment{sourceID: "target-plugin", resources: map[string]domainmigration.Resource{}}
	operations := roundTripTarget{environment: target}
	executor := NewExecutor(NewCapabilityResolver(executionCandidates{}), fakeFactory{operations: operations}, &evidenceSink{})
	workflow := &workflowSink{}
	validator := NewRoundTripValidatorWithProvenance(NewDiscoverer(), NewPlanner(), executor, workflow)
	ctx := observability.ContextWithTraceID(context.Background(), "round-trip-trace")

	result, err := validator.Validate(ctx, source, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Plan.Steps) != 2 || len(result.Execution.Evidence) != 2 || len(result.Target.Resources) != 2 {
		t.Fatalf("incomplete round-trip result: %+v", result)
	}
	for _, evidence := range result.Execution.Evidence {
		if evidence.Status != StepConverged || !evidence.Verified || evidence.TraceID != "round-trip-trace" {
			t.Fatalf("unverified execution evidence: %+v", evidence)
		}
	}
	if len(workflow.records) != 1 || workflow.records[0].SourcePluginID != "source-plugin" || workflow.records[0].FinalState != "verified" || workflow.records[0].ObservationCount != 4 || workflow.records[0].VerificationCount != 4 {
		t.Fatalf("workflow provenance=%+v", workflow.records)
	}
}

func TestValidateSemanticRoundTripRejectsDriftAndPartialDiscovery(t *testing.T) {
	source := domainmigration.Aggregate{Discovery: domainmigration.DiscoveryComplete, Resources: []domainmigration.Resource{roundTripResource("api")}}
	target := source.Canonical()
	target.Resources[0].Origin = domainmigration.ResourceOrigin{Source: "other", NativeType: "container", NativeID: "native-api"}
	if err := ValidateSemanticRoundTrip(source, target); err != nil {
		t.Fatalf("native identity must not create semantic drift: %v", err)
	}
	target.Resources[0].Attributes["tier"] = "changed"
	if err := ValidateSemanticRoundTrip(source, target); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("semantic drift accepted: %v", err)
	}
	partial := source.Canonical()
	partial.Discovery = domainmigration.DiscoveryPartial
	partial.Unknowns = []domainmigration.UnknownObservation{{Source: "target", Kind: "permission-denied", Scope: "services", Reason: "denied"}}
	if err := ValidateSemanticRoundTrip(source, partial); err == nil || !strings.Contains(err.Error(), "complete") {
		t.Fatalf("partial target accepted: %v", err)
	}
}

func TestRoundTripValidatorFailsClosedForMissingDependencies(t *testing.T) {
	validator := NewRoundTripValidator(nil, nil, nil)
	if _, err := validator.Validate(context.Background(), nil, nil); err == nil {
		t.Fatal("missing round-trip dependencies accepted")
	}
}

func TestSemanticRoundTripPreservesAllDeclaredPropertiesAndRequirements(t *testing.T) {
	properties := map[string]string{"cpu": "4", "memory": "8Gi", "architecture": "amd64", "firmware": "uefi", "disks": "root,data", "storage": "persistent", "network": "private", "ports": "443", "persistence": "required", "health": "ready", "identity": "workload-a", "availability": "multi-zone", "isolation": "microvm", "durability": "replicated"}
	requirements := []domainmigration.Requirement{{Capability: "secrets.reference", Version: "v1"}, {Capability: "migration.apply", Version: "v1"}}
	resource := roundTripResource("workload")
	resource.Attributes, resource.Requirements = properties, requirements
	source := domainmigration.Aggregate{Discovery: domainmigration.DiscoveryComplete, Resources: []domainmigration.Resource{resource}}
	source.ExternalDependencies = []domainmigration.ExternalDependency{{ResourceID: "workload", Kind: "managed-database", Reference: "external://database/primary", Required: true}}
	source.Transformations = []domainmigration.Transformation{{ResourceID: "workload", Field: "architecture", From: "x86_64", To: "amd64", Reason: "canonical architecture spelling"}}
	target := source.Canonical()
	target.Resources[0].Origin = domainmigration.ResourceOrigin{Source: "target", NativeType: "native", NativeID: "target/workload"}
	if err := ValidateSemanticRoundTrip(source, target); err != nil {
		t.Fatal(err)
	}
	for property := range properties {
		drifted := target.Canonical()
		drifted.Resources[0].Attributes[property] = "changed"
		if err := ValidateSemanticRoundTrip(source, drifted); err == nil {
			t.Fatalf("property drift accepted: %s", property)
		}
	}
	drifted := target.Canonical()
	drifted.Resources[0].Requirements = drifted.Resources[0].Requirements[1:]
	if err := ValidateSemanticRoundTrip(source, drifted); err == nil {
		t.Fatal("secret requirement loss accepted")
	}
	drifted = target.Canonical()
	drifted.ExternalDependencies[0].Reference = "external://database/other"
	if err := ValidateSemanticRoundTrip(source, drifted); err == nil {
		t.Fatal("external dependency drift accepted")
	}
	drifted = target.Canonical()
	drifted.Transformations[0].To = "arm64"
	if err := ValidateSemanticRoundTrip(source, drifted); err == nil {
		t.Fatal("transformation drift accepted")
	}
}

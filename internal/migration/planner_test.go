package migration

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func validAggregate() Aggregate {
	return Aggregate{
		Discovery: DiscoveryPartial,
		Resources: []Resource{
			{ID: "app", Kind: "service", Origin: ResourceOrigin{Source: "source-a", NativeType: "service", NativeID: "native-app"}, Attributes: map[string]string{"region": "eu"}, Requirements: []Requirement{{Capability: "migration.apply", Version: "v1"}, {Capability: "artifact.import", Version: "v1"}}},
			{ID: "db", Kind: "database", Origin: ResourceOrigin{Source: "source-a", NativeType: "database", NativeID: "native-db"}, Requirements: []Requirement{{Capability: "migration.apply", Version: "v1"}}},
		},
		Edges:    []DependencyEdge{{From: "app", To: "db", Relation: "depends_on", Required: true}},
		Unknowns: []UnknownObservation{{Source: "source-a", Kind: "opaque", Scope: "network", Reason: "not representable"}},
		Gaps:     []CompatibilityGap{{ResourceID: "app", Requirement: "migration.apply", Compatibility: CompatibilityDegraded, Reason: "manual translation", RequiresApproval: true}},
	}
}

func TestBuildPlanDeterministicDAGAndStableOperationIDs(t *testing.T) {
	input := validAggregate()
	original := validAggregate()
	plan, err := BuildPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 3 || plan.Steps[0].ResourceID != "db" {
		t.Fatalf("steps=%+v, want prerequisite db first and 3 steps", plan.Steps)
	}
	for _, step := range plan.Steps {
		if !strings.HasPrefix(string(step.OperationID), "migration-") || len(step.OperationID) != len("migration-")+64 {
			t.Fatalf("invalid stable operation ID %q", step.OperationID)
		}
		if step.ResourceID == "app" && len(step.DependsOn) != 1 {
			t.Fatalf("app dependency operation IDs=%v", step.DependsOn)
		}
	}
	if plan.Digest == "" || plan.InputDigest == "" {
		t.Fatal("plan and input digests must be set")
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatal("planning mutated its input")
	}

	permuted := validAggregate()
	permuted.Resources[0].Requirements[0], permuted.Resources[0].Requirements[1] = permuted.Resources[0].Requirements[1], permuted.Resources[0].Requirements[0]
	permuted.Resources[0], permuted.Resources[1] = permuted.Resources[1], permuted.Resources[0]
	got, err := BuildPlan(permuted)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan, got) {
		t.Fatalf("permutation changed plan:\n%+v\n%+v", plan, got)
	}
}

func TestBuildPlanRejectsInvalidGraphsBeforeProducingPlan(t *testing.T) {
	tests := map[string]func(*Aggregate){
		"dangling": func(a *Aggregate) { a.Edges[0].To = "missing" },
		"cycle": func(a *Aggregate) {
			a.Edges = append(a.Edges, DependencyEdge{From: "db", To: "app", Relation: "depends_on", Required: true})
		},
		"duplicate resource": func(a *Aggregate) { a.Resources = append(a.Resources, a.Resources[0]) },
		"duplicate requirement": func(a *Aggregate) {
			a.Resources[0].Requirements = append(a.Resources[0].Requirements, a.Resources[0].Requirements[0])
		},
		"secret attribute":        func(a *Aggregate) { a.Resources[0].Attributes["access_token"] = "sentinel" },
		"invalid compatibility":   func(a *Aggregate) { a.Gaps[0].Compatibility = "maybe" },
		"unknown gap requirement": func(a *Aggregate) { a.Gaps[0].Requirement = "missing.capability" },
		"duplicate gap":           func(a *Aggregate) { a.Gaps = append(a.Gaps, a.Gaps[0]) },
		"duplicate unknown":       func(a *Aggregate) { a.Unknowns = append(a.Unknowns, a.Unknowns[0]) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := validAggregate()
			mutate(&input)
			plan, err := BuildPlan(input)
			if err == nil || !IsValidationError(err) {
				t.Fatalf("plan=%+v err=%v, want validation error", plan, err)
			}
			if !reflect.DeepEqual(plan, Plan{}) {
				t.Fatalf("invalid input produced partial plan %+v", plan)
			}
		})
	}
}

func TestPlanPreservesEdgesAndRequiredDependencySemantics(t *testing.T) {
	input := validAggregate()
	input.Edges = append(input.Edges,
		DependencyEdge{From: "app", To: "db", Relation: "requires_storage", Required: true},
		DependencyEdge{From: "app", To: "db", Relation: "co_located", Required: false},
	)
	plan, err := BuildPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Edges) != 3 {
		t.Fatalf("edges=%+v, want all required and optional relations", plan.Edges)
	}
	for _, step := range plan.Steps {
		if step.ResourceID == "app" && len(step.DependsOn) != 1 {
			t.Fatalf("multiple required relations duplicated dependency: %+v", step.DependsOn)
		}
	}

	optionalOnly := validAggregate()
	optionalOnly.Edges[0].Required = false
	optionalPlan, err := BuildPlan(optionalOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range optionalPlan.Steps {
		if step.ResourceID == "app" && len(step.DependsOn) != 0 {
			t.Fatalf("optional edge constrained execution: %+v", step.DependsOn)
		}
	}
	if optionalPlan.Steps[0].ResourceID != "app" {
		t.Fatalf("optional edge constrained scheduling order: %+v", optionalPlan.Steps)
	}

	withoutOperation := validAggregate()
	withoutOperation.Resources = append(withoutOperation.Resources, Resource{ID: "network", Kind: "network", Origin: ResourceOrigin{Source: "source-a", NativeType: "network", NativeID: "native-network"}})
	withoutOperation.Edges = append(withoutOperation.Edges, DependencyEdge{From: "app", To: "network", Relation: "attached_to", Required: true})
	withoutOperationPlan, err := BuildPlan(withoutOperation)
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutOperationPlan.Edges) != 2 {
		t.Fatalf("edge to resource without operation disappeared: %+v", withoutOperationPlan.Edges)
	}
}

func TestPlanValidateDetectsPostBuildMutation(t *testing.T) {
	plan, err := BuildPlan(validAggregate())
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("built plan invalid: %v", err)
	}
	plan.Steps[0].Action = "delete"
	if err := plan.Validate(); err == nil || !IsValidationError(err) {
		t.Fatalf("mutated plan accepted: %v", err)
	}
	plan, err = BuildPlan(validAggregate())
	if err != nil {
		t.Fatal(err)
	}
	plan.Gaps[0].Reason = "different but structurally valid reason"
	if err := plan.VerifyDigest(); err == nil || !IsValidationError(err) {
		t.Fatalf("digest did not detect mutation: %v", err)
	}
}

func TestPlanValidateRejectsResealedDerivedStateMutations(t *testing.T) {
	tests := map[string]func(*Plan){
		"operation ID":  func(plan *Plan) { plan.Steps[0].OperationID = "migration-arbitrary" },
		"dangling edge": func(plan *Plan) { plan.Edges[0].To = "missing" },
		"missing required dependency": func(plan *Plan) {
			for i := range plan.Steps {
				if plan.Steps[i].ResourceID == "app" {
					plan.Steps[i].DependsOn = nil
				}
			}
		},
		"arbitrary input digest": func(plan *Plan) { plan.InputDigest = "sha256:arbitrary" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			plan, err := BuildPlan(validAggregate())
			if err != nil {
				t.Fatal(err)
			}
			mutate(&plan)
			plan.Digest, err = plan.ComputeDigest()
			if err != nil {
				t.Fatal(err)
			}
			if err := plan.Validate(); err == nil || !IsValidationError(err) {
				t.Fatalf("resealed mutation accepted: %v", err)
			}
		})
	}
}

func TestBuildPlanLexicallyBreaksIndependentResourceTies(t *testing.T) {
	input := validAggregate()
	input.Edges = nil
	plan, err := BuildPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Steps[0].ResourceID != "app" || plan.Steps[2].ResourceID != "db" {
		t.Fatalf("non-lexical resource ordering: %+v", plan.Steps)
	}
}

func TestAggregateLimitsAndFailedDiscoveryFailClosed(t *testing.T) {
	overLimit := validAggregate()
	overLimit.Resources = make([]Resource, MaxResources+1)
	if _, err := BuildPlan(overLimit); err == nil || !IsValidationError(err) {
		t.Fatalf("over-limit resources: %v", err)
	}

	failed := validAggregate()
	failed.Discovery = DiscoveryFailed
	if plan, err := BuildPlan(failed); err == nil || !reflect.DeepEqual(plan, Plan{}) {
		t.Fatalf("failed discovery produced plan=%+v err=%v", plan, err)
	}

	partialWithoutUnknown := validAggregate()
	partialWithoutUnknown.Unknowns = nil
	if _, err := BuildPlan(partialWithoutUnknown); err == nil || !IsValidationError(err) {
		t.Fatalf("partial discovery without unknown: %v", err)
	}
}

func TestAggregateValidationRejectsMalformedDomainValues(t *testing.T) {
	tests := map[string]func(*Aggregate){
		"discovery status": func(a *Aggregate) { a.Discovery = "unknown" },
		"resource ID":      func(a *Aggregate) { a.Resources[0].ID = "" },
		"resource kind":    func(a *Aggregate) { a.Resources[0].Kind = "\x00" },
		"origin source":    func(a *Aggregate) { a.Resources[0].Origin.Source = "" },
		"origin type":      func(a *Aggregate) { a.Resources[0].Origin.NativeType = "" },
		"origin ID":        func(a *Aggregate) { a.Resources[0].Origin.NativeID = "" },
		"duplicate origin": func(a *Aggregate) { a.Resources[1].Origin = a.Resources[0].Origin },
		"empty requirement capability": func(a *Aggregate) {
			a.Resources[0].Requirements[0].Capability = ""
		},
		"empty requirement version": func(a *Aggregate) { a.Resources[0].Requirements[0].Version = "" },
		"edge empty from":           func(a *Aggregate) { a.Edges[0].From = "" },
		"edge self-reference":       func(a *Aggregate) { a.Edges[0].To = a.Edges[0].From },
		"edge unknown from":         func(a *Aggregate) { a.Edges[0].From = "missing" },
		"duplicate edge":            func(a *Aggregate) { a.Edges = append(a.Edges, a.Edges[0]) },
		"unknown source":            func(a *Aggregate) { a.Unknowns[0].Source = "" },
		"unknown reason":            func(a *Aggregate) { a.Unknowns[0].Reason = strings.Repeat("x", 1025) },
		"gap unknown resource":      func(a *Aggregate) { a.Gaps[0].ResourceID = "missing" },
		"gap empty reason":          func(a *Aggregate) { a.Gaps[0].Reason = "" },
		"gap invalid lost guarantee": func(a *Aggregate) {
			a.Gaps[0].LostGuarantee = "bad\x00guarantee"
		},
		"attribute invalid key":   func(a *Aggregate) { a.Resources[0].Attributes["bad\x00key"] = "value" },
		"attribute invalid value": func(a *Aggregate) { a.Resources[0].Attributes["description"] = strings.Repeat("x", 1025) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := validAggregate()
			mutate(&input)
			if err := input.Validate(); err == nil || !IsValidationError(err) {
				t.Fatalf("got %v, want validation error", err)
			}
		})
	}
}

func TestAggregateValidationEnforcesCollectionLimits(t *testing.T) {
	tests := map[string]func(*Aggregate){
		"edges":        func(a *Aggregate) { a.Edges = make([]DependencyEdge, MaxEdges+1) },
		"unknowns":     func(a *Aggregate) { a.Unknowns = make([]UnknownObservation, MaxUnknowns+1) },
		"gaps":         func(a *Aggregate) { a.Gaps = make([]CompatibilityGap, MaxGaps+1) },
		"requirements": func(a *Aggregate) { a.Resources[0].Requirements = make([]Requirement, MaxRequirements+1) },
		"attributes": func(a *Aggregate) {
			a.Resources[0].Attributes = make(map[string]string, MaxAttributes+1)
			for i := 0; i <= MaxAttributes; i++ {
				a.Resources[0].Attributes[fmt.Sprintf("attribute-%04d", i)] = "value"
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := validAggregate()
			mutate(&input)
			if err := input.Validate(); err == nil || !IsValidationError(err) {
				t.Fatalf("got %v, want collection limit failure", err)
			}
		})
	}
}

func TestPlanDigestAndCanonicalValidationFailures(t *testing.T) {
	plan, err := BuildPlan(validAggregate())
	if err != nil {
		t.Fatal(err)
	}
	withoutDigest := plan
	withoutDigest.Digest = ""
	if err := withoutDigest.VerifyDigest(); err == nil || !IsValidationError(err) {
		t.Fatalf("missing digest accepted: %v", err)
	}

	nonCanonical := plan
	nonCanonical.Resources[0], nonCanonical.Resources[1] = nonCanonical.Resources[1], nonCanonical.Resources[0]
	nonCanonical.Digest, err = nonCanonical.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	if err := nonCanonical.Validate(); err == nil || !IsValidationError(err) {
		t.Fatalf("resealed non-canonical plan accepted: %v", err)
	}

	failed := plan
	failed.Discovery = DiscoveryFailed
	failed.Digest, err = failed.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	if err := failed.Validate(); err == nil || !IsValidationError(err) {
		t.Fatalf("failed-discovery plan accepted: %v", err)
	}
}

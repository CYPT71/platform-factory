package migration

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestResourceRejectsInvalidStructureAndHostileAttributes(t *testing.T) {
	tests := map[string]func(*Resource){
		"missing source plugin":      func(r *Resource) { r.Source.PluginID = "" },
		"missing source native type": func(r *Resource) { r.Source.NativeType = "" },
		"missing source native ID":   func(r *Resource) { r.Source.NativeID = "" },
		"too many requirements": func(r *Resource) {
			r.Requirements = make([]Requirement, MaxRequirements+1)
		},
		"too many top-level attributes": func(r *Resource) {
			r.Attributes = make(map[string]interface{}, MaxAttributes+1)
			for i := 0; i <= MaxAttributes; i++ {
				r.Attributes[fmt.Sprintf("field_%d", i)] = i
			}
		},
		"invalid attribute key": func(r *Resource) { r.Attributes = map[string]interface{}{" ": true} },
		"nul attribute key":     func(r *Resource) { r.Attributes = map[string]interface{}{"bad\x00key": true} },
		"nested invalid key": func(r *Resource) {
			r.Attributes = map[string]interface{}{"config": map[string]interface{}{" ": true}}
		},
		"unsupported attribute value": func(r *Resource) {
			r.Attributes = map[string]interface{}{"channel": make(chan int)}
		},
		"excessive attribute depth": func(r *Resource) {
			var value interface{} = "leaf"
			for i := 0; i <= maxAttributeDepth; i++ {
				value = []interface{}{value}
			}
			r.Attributes = map[string]interface{}{"nested": value}
		},
		"excessive attribute nodes": func(r *Resource) {
			values := make([]interface{}, MaxAttributes)
			for i := range values {
				values[i] = i
			}
			r.Attributes = map[string]interface{}{"values": values}
		},
		"invalid requirement": func(r *Resource) {
			r.Requirements = []Requirement{{Capability: "apply"}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			r := TestResource("resource", "vm")
			mutate(&r)
			if err := r.Validate(); !IsValidationError(err) {
				t.Fatalf("expected validation error, got %v", err)
			}
		})
	}
}

func TestResourceAcceptsSupportedNestedAttributeTypes(t *testing.T) {
	r := TestResource("resource", "vm")
	r.Attributes = map[string]interface{}{
		"values": []interface{}{nil, true, "text", float32(1), float64(2), int8(3), int16(4), int32(5), int64(6), uint(7), uint8(8), uint16(9), uint32(10), uint64(11)},
		"nested": map[string]interface{}{"enabled": true},
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("supported canonical values rejected: %v", err)
	}
}

func TestGraphIdentifiersRejectEveryIncompleteComponent(t *testing.T) {
	valid := TestResourceID("plugin", "vm", "native")
	edgeTests := map[string]func(*DependencyEdge){
		"from type": func(e *DependencyEdge) { e.From.NativeType = "" },
		"from ID":   func(e *DependencyEdge) { e.From.NativeID = "" },
		"to plugin": func(e *DependencyEdge) { e.To.PluginID = "" },
		"to type":   func(e *DependencyEdge) { e.To.NativeType = "" },
		"to ID":     func(e *DependencyEdge) { e.To.NativeID = "" },
	}
	for name, mutate := range edgeTests {
		t.Run("edge "+name, func(t *testing.T) {
			e := TestDependencyEdge(valid, valid, "depends_on")
			mutate(&e)
			if err := e.Validate(); !IsValidationError(err) {
				t.Fatalf("expected validation error, got %v", err)
			}
		})
	}
	for name, mutate := range map[string]func(*ResourceID){
		"plugin": func(id *ResourceID) { id.PluginID = "" },
		"type":   func(id *ResourceID) { id.NativeType = "" },
		"ID":     func(id *ResourceID) { id.NativeID = "" },
	} {
		t.Run("resource ID "+name, func(t *testing.T) {
			id := valid
			mutate(&id)
			if err := id.Validate(); !IsValidationError(err) {
				t.Fatalf("expected validation error, got %v", err)
			}
		})
	}
}

func TestMigrationPlanRejectsEverySemanticInconsistency(t *testing.T) {
	tests := map[string]func(*MigrationPlan){
		"invalid resource":               func(p *MigrationPlan) { p.Resources[0].Kind = "" },
		"duplicate origin":               func(p *MigrationPlan) { p.Resources[1].Source = p.Resources[0].Source },
		"invalid dependency":             func(p *MigrationPlan) { p.Graph[0].Relation = "" },
		"unknown dependency from":        func(p *MigrationPlan) { p.Graph[0].From.NativeID = "missing" },
		"duplicate dependency":           func(p *MigrationPlan) { p.Graph = append(p.Graph, p.Graph[0]) },
		"empty operation ID":             func(p *MigrationPlan) { p.Steps[0].OperationID = "" },
		"invalid step resource":          func(p *MigrationPlan) { p.Steps[0].ResourceID.PluginID = "" },
		"unknown step resource":          func(p *MigrationPlan) { p.Steps[0].ResourceID.NativeID = "missing" },
		"empty step capability":          func(p *MigrationPlan) { p.Steps[0].Capability = "" },
		"empty step action":              func(p *MigrationPlan) { p.Steps[0].Action = "" },
		"empty step status":              func(p *MigrationPlan) { p.Steps[0].Status = "" },
		"empty gap resource":             func(p *MigrationPlan) { p.Gaps[0].ResourceID = "" },
		"unknown gap resource":           func(p *MigrationPlan) { p.Gaps[0].ResourceID = "missing" },
		"empty unknown source":           func(p *MigrationPlan) { p.Unknowns[0].SourcePlugin = "" },
		"empty unknown observation type": func(p *MigrationPlan) { p.Unknowns[0].ObservationType = "" },
		"empty unknown scope":            func(p *MigrationPlan) { p.Unknowns[0].Scope = "" },
		"empty unknown reason":           func(p *MigrationPlan) { p.Unknowns[0].Reason = "" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := validCanonicalPlan(t)
			mutate(&p)
			if _, err := p.ComputeDigest(); !IsValidationError(err) {
				t.Fatalf("expected validation error, got %v", err)
			}
		})
	}
}

func TestMigrationPlanCollectionLimitAndSetDigestFailure(t *testing.T) {
	p := MigrationPlan{
		Version:         FormatVersion,
		DiscoveryStatus: DiscoveryStatusComplete,
		Resources:       make([]Resource, MaxResources+1),
	}
	if err := p.SetDigest(); !IsValidationError(err) {
		t.Fatalf("expected collection-limit validation error, got %v", err)
	}
	if p.Digest != "" {
		t.Fatalf("failed seal changed digest to %q", p.Digest)
	}
}

func TestValidationErrorClassificationTraversesWrapping(t *testing.T) {
	validation := &ValidationError{Message: "invalid"}
	if !IsValidationError(fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", validation))) {
		t.Fatal("wrapped validation error was not classified")
	}
	if IsValidationError(errors.New("ordinary failure")) {
		t.Fatal("ordinary error classified as validation")
	}
	if IsValidationError(nil) {
		t.Fatal("nil classified as validation")
	}
	if !strings.Contains(validation.Error(), "invalid") {
		t.Fatalf("validation error lost its message: %q", validation.Error())
	}
}

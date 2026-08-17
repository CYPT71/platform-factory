package v1

import (
	"reflect"
	"strings"
	"testing"
)

func TestYAMLSerialization(t *testing.T) {
	// Test Resource serialization
	resource := &Resource{
		ID:   "test-resource-1",
		Kind: "vm",
		Source: ResourceOrigin{
			PluginID:   "test-plugin",
			NativeType: "aws-instance",
			NativeID:   "i-1234567890abcdef0",
			Location:   "us-east-1",
		},
		Attributes: map[string]interface{}{
			"cpu": 4,
			"mem": "8GB",
		},
		Requirements: []Requirement{
			{
				Capability: "apply",
				Version:    "v1",
			},
		},
	}

	yamlData, err := MarshalYAML(resource)
	if err != nil {
		t.Fatalf("Failed to marshal Resource to YAML: %v", err)
	}

	var unmarshaledResource Resource
	err = UnmarshalYAML(yamlData, &unmarshaledResource)
	if err != nil {
		t.Fatalf("Failed to unmarshal Resource from YAML: %v", err)
	}

	if !reflect.DeepEqual(*resource, unmarshaledResource) {
		t.Errorf("Resource round-trip failed. Expected: %+v, got: %+v", *resource, unmarshaledResource)
	}

	// Test DependencyEdge serialization
	edge := &DependencyEdge{
		From: ResourceID{
			PluginID:   "plugin-a",
			NativeType: "vm",
			NativeID:   "i-1234567890abcdef0",
		},
		To: ResourceID{
			PluginID:   "plugin-b",
			NativeType: "disk",
			NativeID:   "vol-1234567890abcdef0",
		},
		Relation: "requires",
		Required: true,
	}

	yamlData, err = MarshalYAML(edge)
	if err != nil {
		t.Fatalf("Failed to marshal DependencyEdge to YAML: %v", err)
	}

	var unmarshaledEdge DependencyEdge
	err = UnmarshalYAML(yamlData, &unmarshaledEdge)
	if err != nil {
		t.Fatalf("Failed to unmarshal DependencyEdge from YAML: %v", err)
	}

	if !reflect.DeepEqual(*edge, unmarshaledEdge) {
		t.Errorf("DependencyEdge round-trip failed. Expected: %+v, got: %+v", *edge, unmarshaledEdge)
	}

	// Test MigrationPlan serialization
	plan := &MigrationPlan{
		Version:         FormatVersion,
		DiscoveryStatus: DiscoveryStatusPartial,
		Resources:       []Resource{*resource},
		// The edge above is tested independently and intentionally references a
		// different fixture graph. Keep this plan internally valid: canonical
		// serialization now rejects invalid plans before any bytes are written.
		Graph: nil,
		Steps: []MigrationStep{
			{
				OperationID: "step-1",
				ResourceID: ResourceID{
					PluginID:   "test-plugin",
					NativeType: resource.Source.NativeType,
					NativeID:   "i-1234567890abcdef0",
				},
				Capability: "apply",
				Action:     "create",
				Status:     "pending",
			},
		},
		Gaps: []CompatibilityGap{
			{
				ResourceID:       resource.ID,
				Requirement:      "runtime.vm.apply",
				Reason:           "version mismatch",
				Status:           CompatibilityDegraded,
				LostGuarantee:    "live migration",
				RequiresApproval: true,
			},
		},
		Unknowns: []UnknownObservation{
			{
				SourcePlugin:    "test-plugin",
				ObservationType: "unknown-type",
				Scope:           "compute",
				Reason:          "unknown native type",
			},
		},
		Digest: "abc123def456",
	}

	yamlData, err = MarshalYAML(plan)
	if err != nil {
		t.Fatalf("Failed to marshal MigrationPlan to YAML: %v", err)
	}

	var unmarshaledPlan MigrationPlan
	err = UnmarshalYAML(yamlData, &unmarshaledPlan)
	if err != nil {
		t.Fatalf("Failed to unmarshal MigrationPlan from YAML: %v", err)
	}

	if !reflect.DeepEqual(*plan, unmarshaledPlan) {
		t.Errorf("MigrationPlan round-trip failed. Expected: %+v, got: %+v", *plan, unmarshaledPlan)
	}
}

func TestYAMLDeterminism(t *testing.T) {
	resource := &Resource{
		ID:   "test-resource-1",
		Kind: "vm",
		Source: ResourceOrigin{
			PluginID:   "test-plugin",
			NativeType: "aws-instance",
			NativeID:   "i-1234567890abcdef0",
			Location:   "us-east-1",
		},
		Attributes: map[string]interface{}{
			"cpu": 4,
			"mem": "8GB",
		},
		Requirements: []Requirement{
			{
				Capability: "apply",
				Version:    "v1",
			},
		},
	}

	yamlData1, err := MarshalYAML(resource)
	if err != nil {
		t.Fatalf("Failed to marshal Resource to YAML: %v", err)
	}

	yamlData2, err := MarshalYAML(resource)
	if err != nil {
		t.Fatalf("Failed to marshal Resource to YAML: %v", err)
	}

	if string(yamlData1) != string(yamlData2) {
		t.Errorf("YAML serialization is not deterministic")
	}
}

func TestUnmarshalYAMLRejectsHostileDocuments(t *testing.T) {
	tests := map[string][]byte{
		"oversized":          []byte(strings.Repeat("x", MaxCanonicalYAMLBytes+1)),
		"malformed first":    []byte("version: [\n"),
		"multiple documents": []byte("version: v1\n---\nversion: v1\n"),
		"malformed trailing": []byte("version: v1\n---\nversion: [\n"),
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			var got MigrationPlan
			if err := UnmarshalYAML(document, &got); err == nil {
				t.Fatal("hostile canonical document accepted")
			}
		})
	}
}

func TestMarshalYAMLCanonicalizesPlanWithoutMutatingCaller(t *testing.T) {
	p := validCanonicalPlan(t)
	originalFirst := p.Resources[0].ID
	b, err := MarshalYAML(&p)
	if err != nil {
		t.Fatal(err)
	}
	if p.Resources[0].ID != originalFirst {
		t.Fatal("marshal mutated caller ordering")
	}
	if !strings.Contains(string(b), "digest: "+p.Digest) {
		t.Fatal("marshal dropped sealed plan digest")
	}
}

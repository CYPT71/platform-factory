package migration

import (
	"testing"
)

func TestResourceValidate(t *testing.T) {
	// Valid resource should pass validation
	validResource := TestResource("test-id", "vm")
	if err := validResource.Validate(); err != nil {
		t.Errorf("Valid resource failed validation: %v", err)
	}

	// Resource with empty ID should fail validation
	invalidResource := TestResource("", "vm")
	if err := invalidResource.Validate(); !IsValidationError(err) {
		t.Errorf("Resource with empty ID should fail validation, got: %v", err)
	}

	// Resource with empty kind should fail validation
	invalidResource2 := TestResource("test-id", "")
	if err := invalidResource2.Validate(); !IsValidationError(err) {
		t.Errorf("Resource with empty kind should fail validation, got: %v", err)
	}
}

func TestRequirementValidate(t *testing.T) {
	// Valid requirement should pass validation
	validReq := Requirement{
		Capability: "discover",
		Version:    "1.0.0",
	}
	if err := validReq.Validate(); err != nil {
		t.Errorf("Valid requirement failed validation: %v", err)
	}

	// Requirement with empty capability should fail validation
	invalidReq2 := Requirement{
		Capability: "",
		Version:    "1.0.0",
	}
	if err := invalidReq2.Validate(); !IsValidationError(err) {
		t.Errorf("Requirement with empty capability should fail validation, got: %v", err)
	}

	// Requirement with empty version should fail validation
	invalidReq3 := Requirement{
		Capability: "discover",
		Version:    "",
	}
	if err := invalidReq3.Validate(); !IsValidationError(err) {
		t.Errorf("Requirement with empty version should fail validation, got: %v", err)
	}
}

func TestDependencyEdgeValidate(t *testing.T) {
	from := TestResourceID("from-plugin", "vm", "vm-1")
	to := TestResourceID("to-plugin", "disk", "disk-1")

	// Valid dependency edge should pass validation
	validEdge := TestDependencyEdge(from, to, "depends_on")
	if err := validEdge.Validate(); err != nil {
		t.Errorf("Valid dependency edge failed validation: %v", err)
	}

	// Edge with empty from plugin ID should fail validation
	invalidFrom := TestDependencyEdge(
		TestResourceID("", "vm", "vm-1"),
		to,
		"depends_on",
	)
	if err := invalidFrom.Validate(); !IsValidationError(err) {
		t.Errorf("Dependency edge with empty from plugin ID should fail validation, got: %v", err)
	}

	// Edge with empty relation should fail validation
	invalidRel := TestDependencyEdge(from, to, "")
	if err := invalidRel.Validate(); !IsValidationError(err) {
		t.Errorf("Dependency edge with empty relation should fail validation, got: %v", err)
	}
}

func TestMigrationPlanValidate(t *testing.T) {
	// Valid migration plan should pass validation
	validPlan := MigrationPlan{
		Version:         FormatVersion,
		DiscoveryStatus: DiscoveryStatusComplete,
		Resources:       []Resource{TestResource("vm-1", "vm"), TestResource("disk-1", "disk")},
		Graph: []DependencyEdge{
			TestDependencyEdge(
				TestResourceID("test-plugin", "test-type", "vm-1"),
				TestResourceID("test-plugin", "test-type", "disk-1"),
				"depends_on",
			),
		},
		Steps: []MigrationStep{
			{
				OperationID: "op-1",
				ResourceID:  TestResourceID("test-plugin", "test-type", "vm-1"),
				Capability:  "discover",
				Action:      "create",
				Status:      "pending",
			},
		},
	}
	if err := validPlan.SetDigest(); err != nil {
		t.Fatal(err)
	}
	if err := validPlan.Validate(); err != nil {
		t.Errorf("Valid migration plan failed validation: %v", err)
	}

	// Migration plan with empty version should fail validation
	invalidPlan := MigrationPlan{
		Version:         "",
		DiscoveryStatus: DiscoveryStatusComplete,
		Digest:          "test-digest",
	}
	if err := invalidPlan.Validate(); !IsValidationError(err) {
		t.Errorf("Migration plan with empty version should fail validation, got: %v", err)
	}

	// Migration plan with empty digest should fail validation
	invalidPlan2 := MigrationPlan{
		Version:         "v1",
		DiscoveryStatus: DiscoveryStatusComplete,
		Digest:          "",
	}
	if err := invalidPlan2.Validate(); !IsValidationError(err) {
		t.Errorf("Migration plan with empty digest should fail validation, got: %v", err)
	}

	// Migration plan with empty digest should fail validation
	validPlanWithEmptyDigest := MigrationPlan{
		Version:         "v1",
		DiscoveryStatus: DiscoveryStatusComplete,
		Digest:          "",
		Resources: []Resource{
			{
				ID:   "test-resource",
				Kind: "vm",
				Source: ResourceOrigin{
					PluginID:   "plugin-a",
					NativeType: "vm",
					NativeID:   "vm-1",
				},
				Attributes:   map[string]interface{}{},
				Requirements: []Requirement{},
			},
		},
		Steps:    []MigrationStep{},
		Gaps:     []CompatibilityGap{},
		Unknowns: []UnknownObservation{},
	}
	if err := validPlanWithEmptyDigest.Validate(); !IsValidationError(err) {
		t.Errorf("Migration plan with empty digest should fail validation, got: %v", err)
	}
}

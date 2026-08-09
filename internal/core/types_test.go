package core

import (
	"strings"
	"testing"
)

func TestCanonicalCoreTypesExist(t *testing.T) {
	_ = WorkloadID("")
	_ = OperationID("")
	_ = ArtifactDigest("")
	_ = PluginID("")
	_ = WorkloadSpec{}
	_ = BuildPlan{}
	_ = ArtifactDescriptor{}
	_ = RuntimeSpec{}
	_ = DeploymentSpec{}
	_ = RuntimeState{}
	_ = EvidenceBundle{}
	_ = CapabilitySet{}
	_ = PolicyDecision{}
}

func TestWorkloadSpecValidateRequiresIDAndVersion(t *testing.T) {
	spec := WorkloadSpec{}
	if err := spec.Validate(); err == nil {
		t.Fatal("expected validation error for empty workload spec")
	}
}

func TestBuildPlanValidateRequiresWorkloadID(t *testing.T) {
	plan := BuildPlan{}
	if err := plan.Validate(); err == nil {
		t.Fatal("expected validation error for empty build plan")
	}
}

func TestArtifactDescriptorValidateRequiresDigest(t *testing.T) {
	artifact := ArtifactDescriptor{}
	err := artifact.Validate()
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("expected digest validation error, got %v", err)
	}
}

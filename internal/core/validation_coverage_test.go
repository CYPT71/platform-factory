package core

import (
	"strings"
	"testing"
)

func TestCapabilityValidationAndManifestEnforcement(t *testing.T) {
	valid := []string{"deployment.apply", "runtime_vm-create", "A1"}
	for _, capability := range valid {
		if err := ValidateCapability(capability); err != nil || !IsValidCapability(capability) {
			t.Fatalf("valid capability %q rejected: %v", capability, err)
		}
	}
	invalid := []string{"", "deployment/apply", strings.Repeat("a", 65)}
	for _, capability := range invalid {
		if err := ValidateCapability(capability); err == nil || IsValidCapability(capability) {
			t.Fatalf("invalid capability %q accepted", capability)
		}
		manifest := PluginManifest{ID: "plugin", Version: "v1", ProtocolVersion: 1, Family: PluginFamilyCapability, Capabilities: []string{capability}}
		if err := manifest.Validate(); err == nil {
			t.Fatalf("manifest accepted invalid capability %q", capability)
		}
	}
}

func TestCoreValueValidationBranches(t *testing.T) {
	tests := []struct {
		name string
		good func() error
		bad  func() error
	}{
		{"workload", func() error { return (WorkloadSpec{ID: "w", Version: "v1"}).Validate() }, func() error { return (WorkloadSpec{}).Validate() }},
		{"build plan", func() error { return (BuildPlan{WorkloadID: "w"}).Validate() }, func() error { return (BuildPlan{}).Validate() }},
		{"artifact", func() error { return (ArtifactDescriptor{Digest: "sha256:value"}).Validate() }, func() error { return (ArtifactDescriptor{}).Validate() }},
		{"runtime", func() error { return (RuntimeSpec{Kind: "container"}).Validate() }, func() error { return (RuntimeSpec{}).Validate() }},
		{"deployment", func() error { return (DeploymentSpec{Runtime: RuntimeSpec{Kind: "vm"}}).Validate() }, func() error { return (DeploymentSpec{}).Validate() }},
		{"evidence", func() error {
			return (EvidenceBundle{Artifacts: []ArtifactDescriptor{{Digest: "sha256:value"}}}).Validate()
		}, func() error { return (EvidenceBundle{Artifacts: []ArtifactDescriptor{{}}}).Validate() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.good(); err != nil {
				t.Fatalf("valid value rejected: %v", err)
			}
			if err := test.bad(); err == nil {
				t.Fatal("invalid value accepted")
			}
		})
	}
}

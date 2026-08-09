package plugin

import (
	"strings"
	"testing"
)

// TestCapabilityNamesAreWellFormed verifies all standard capability names
// follow the expected format: lowercase alphanumeric with optional dots.
// Backward-compatible capabilities (detect, freeze, plan, scan) don't use dot notation.
func TestCapabilityNamesAreWellFormed(t *testing.T) {
	// Legacy capabilities without dot notation (backward compatible)
	legacyCaps := []string{
		CapabilityDetect,
		CapabilityFreeze,
		CapabilityPlan,
		CapabilityScan,
	}

	// New capabilities with dot notation
	dotCaps := []string{
		// Runtime capabilities
		CapabilityRuntimeCreate,
		CapabilityRuntimeStop,
		CapabilityRuntimeLogs,
		CapabilityRuntimeStatus,
		CapabilityRuntimeExec,
		// Deployment capabilities
		CapabilityDeploymentPlan,
		CapabilityDeploymentApply,
		CapabilityDeploymentObserve,
		CapabilityDeploymentRollback,
		CapabilityDeploymentDelete,
		// Builder capabilities
		CapabilityBuilderBuild,
		CapabilityBuilderTest,
		CapabilityBuilderClean,
		CapabilityBuilderPush,
		// Analyzer capabilities
		CapabilityAnalyzerScan,
		CapabilityAnalyzerAttest,
		CapabilityAnalyzerVerify,
		CapabilityAnalyzerSign,
		// Registry capabilities
		CapabilityRegistryPush,
		CapabilityRegistryPull,
		CapabilityRegistryList,
		CapabilityRegistryDelete,
	}

	// Legacy capabilities should pass a more lenient validation (lowercase alphanumeric only)
	for _, cap := range legacyCaps {
		for _, c := range cap {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
				t.Errorf("legacy capability %q contains invalid characters", cap)
			}
		}
	}

	// New capabilities should pass full validation
	for _, cap := range dotCaps {
		if err := ValidateCapability(cap); err != nil {
			t.Errorf("capability %q is not well-formed: %v", cap, err)
		}
	}
}

// TestCapabilityDotNotation verifies that dot-notation capabilities
// have exactly one dot and valid family/action parts.
func TestCapabilityDotNotation(t *testing.T) {
	dotCaps := []string{
		CapabilityRuntimeCreate,
		CapabilityRuntimeStop,
		CapabilityDeploymentApply,
		CapabilityBuilderBuild,
		CapabilityRegistryPush,
	}

	for _, cap := range dotCaps {
		parts := strings.Split(cap, ".")
		if len(parts) != 2 {
			t.Errorf("capability %q should have exactly one dot, got %d parts", cap, len(parts))
		}
		family, action := parts[0], parts[1]
		if family == "" || action == "" {
			t.Errorf("capability %q has empty family or action", cap)
		}
		// Verify no uppercase
		if family != strings.ToLower(family) || action != strings.ToLower(action) {
			t.Errorf("capability %q should be lowercase", cap)
		}
	}
}

// TestCapabilityFamilies verifies that all capabilities belong to
// expected families.
func TestCapabilityFamilies(t *testing.T) {
	expectedFamilies := []string{
		"", // for backward-compatible capabilities without dot notation
		"runtime",
		"deployment",
		"builder",
		"analyzer",
		"registry",
	}

	familyMap := make(map[string]bool)
	for _, f := range expectedFamilies {
		familyMap[f] = true
	}

	allCaps := []string{
		CapabilityDetect,
		CapabilityFreeze,
		CapabilityPlan,
		CapabilityScan,
		CapabilityRuntimeCreate,
		CapabilityRuntimeStop,
		CapabilityRuntimeLogs,
		CapabilityRuntimeStatus,
		CapabilityRuntimeExec,
		CapabilityDeploymentPlan,
		CapabilityDeploymentApply,
		CapabilityDeploymentObserve,
		CapabilityDeploymentRollback,
		CapabilityDeploymentDelete,
		CapabilityBuilderBuild,
		CapabilityBuilderTest,
		CapabilityBuilderClean,
		CapabilityBuilderPush,
		CapabilityAnalyzerScan,
		CapabilityAnalyzerAttest,
		CapabilityAnalyzerVerify,
		CapabilityAnalyzerSign,
		CapabilityRegistryPush,
		CapabilityRegistryPull,
		CapabilityRegistryList,
		CapabilityRegistryDelete,
	}

	for _, cap := range allCaps {
		var family string
		if idx := strings.Index(cap, "."); idx >= 0 {
			family = cap[:idx]
		}
		if !familyMap[family] {
			t.Errorf("capability %q has unexpected family %q", cap, family)
		}
	}
}

// TestValidateCapabilityRejectsInvalidNames verifies that invalid
// capability names are rejected.
func TestValidateCapabilityRejectsInvalidNames(t *testing.T) {
	invalidCaps := []string{
		"",                     // empty
		".",                    // only dot
		"..",                   // double dot
		".cap",                 // starts with dot
		"cap.",                 // ends with dot
		"Cap",                  // uppercase
		"CAP",                  // all uppercase
		"cap ability",          // space
		"cap/ability",          // slash instead of dot
		"cap_ability",          // underscore
		"cap-ability",          // hyphen
		"runtime.create.extra", // multiple dots
	}

	for _, cap := range invalidCaps {
		if err := ValidateCapability(cap); err == nil {
			t.Errorf("expected ValidateCapability(%q) to return error, got nil", cap)
		}
	}
}

// TestValidateCapabilityAcceptsValidNames verifies that valid
// capability names with dot notation are accepted.
// Note: Legacy capabilities without dots (detect, freeze, etc.) are validated
// separately in TestCapabilityNamesAreWellFormed.
func TestValidateCapabilityAcceptsValidNames(t *testing.T) {
	// Only test capabilities with dot notation - ValidateCapability requires exactly one dot
	validCaps := []string{
		"runtime.create",
		"runtime.stop",
		"deployment.apply",
		"builder.build",
		"analyzer.scan",
		"registry.push",
		"a.b",
		"x1.y2",
	}

	for _, cap := range validCaps {
		if err := ValidateCapability(cap); err != nil {
			t.Errorf("expected ValidateCapability(%q) to return nil, got: %v", cap, err)
		}
	}
}

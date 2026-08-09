package plugin

import (
	"fmt"
)

// HelloResult is what a plugin reports about itself in response to the
// v1.hello handshake call the host makes right after starting a plugin.
type HelloResult struct {
	APIVersion   string   `json:"api_version"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
}

// Standard capability names. A plugin advertises a subset of these in
// its manifest and its v1.hello response; each is dispatched on method
// "v1."+name with the typed params/result pairs below.
//
// "scan" is reserved: its method name is fixed here so third parties do
// not claim it with different semantics, but the host does not call it
// yet. It belongs to the publish/SBOM path, which still orchestrates
// external tooling.
//
// Sanetizer-todo item 11-12: Extended capabilities for capability-based dispatch.
// Capabilities follow dot-notation: {family}.{action} for better organization
// and to enable the core to ask "Who can provide deployment.apply?" instead of
// "Are you KubeVirt?".
const (
	// Language capabilities (original pre-dot-notation for backward compatibility)
	CapabilityDetect = "detect"
	CapabilityFreeze = "freeze"
	CapabilityPlan   = "plan"
	CapabilityScan   = "scan"

	// Runtime capabilities - for runtime environment management
	CapabilityRuntimeCreate = "runtime.create"
	CapabilityRuntimeStop   = "runtime.stop"
	CapabilityRuntimeLogs   = "runtime.logs"
	CapabilityRuntimeStatus = "runtime.status"
	CapabilityRuntimeExec   = "runtime.exec"

	// Deployment capabilities - for workload deployment
	CapabilityDeploymentPlan     = "deployment.plan"
	CapabilityDeploymentApply    = "deployment.apply"
	CapabilityDeploymentObserve  = "deployment.observe"
	CapabilityDeploymentRollback = "deployment.rollback"
	CapabilityDeploymentDelete   = "deployment.delete"

	// Builder capabilities - for artifact construction
	CapabilityBuilderBuild = "builder.build"
	CapabilityBuilderTest  = "builder.test"
	CapabilityBuilderClean = "builder.clean"
	CapabilityBuilderPush  = "builder.push"

	// Analyzer capabilities - for security and compliance
	CapabilityAnalyzerScan   = "analyzer.scan"
	CapabilityAnalyzerAttest = "analyzer.attest"
	CapabilityAnalyzerVerify = "analyzer.verify"
	CapabilityAnalyzerSign   = "analyzer.sign"

	// Registry capabilities - for container registry operations
	CapabilityRegistryPush   = "registry.push"
	CapabilityRegistryPull   = "registry.pull"
	CapabilityRegistryList   = "registry.list"
	CapabilityRegistryDelete = "registry.delete"

	// Migration capabilities are deliberately independent: source-only and
	// target-only plugins are valid protocol participants.
	CapabilityMigrationDiscover        = "migration.discover"
	CapabilityMigrationObserve         = "migration.observe"
	CapabilityMigrationApply           = "migration.apply"
	CapabilityMigrationExport          = "migration.export"
	CapabilityMigrationImport          = "migration.import"
	CapabilityMigrationArtifactObserve = "migration.artifact-observe"
)

// ValidateCapability checks if a capability name is valid.
// Valid capabilities must:
// - Use dot notation: family.action
// - Contain only lowercase alphanumeric characters and dots
// - Not start or end with a dot
// - Have exactly one dot
func ValidateCapability(cap string) error {
	if cap == "" {
		return fmt.Errorf("capability cannot be empty")
	}
	if cap[0] == '.' || cap[len(cap)-1] == '.' {
		return fmt.Errorf("capability cannot start or end with a dot: %q", cap)
	}
	dotCount := 0
	for _, c := range cap {
		if c == '.' {
			dotCount++
			continue
		}
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return fmt.Errorf("capability can only contain lowercase alphanumeric characters and dots: %q", cap)
		}
	}
	if dotCount != 1 {
		return fmt.Errorf("capability must have exactly one dot (family.action): %q", cap)
	}
	return nil
}

// HasCapability checks if a given capability is in the list of supported capabilities.
// This enables capability-based dispatch: "Who can provide deployment.apply?"
// instead of "Are you KubeVirt?".
// Sanetizer-todo item 12: Capability negotiation.
func HasCapability(capabilities []string, cap string) bool {
	for _, c := range capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// DetectParams asks a plugin to classify the project at Path without
// executing anything in it.
type DetectParams struct {
	Path string `json:"path"`
}

// DetectResult reports a plugin's classification. Kind "unknown" means
// the plugin does not recognize the input; the host then consults the
// next plugin. Profile uses the same vocabulary as built-in detection
// (for example "static" for compiled ecosystems).
type DetectResult struct {
	Kind     string   `json:"kind"`
	Profile  string   `json:"profile,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
}

// FreezeParams asks a plugin for the dependency-freeze commands of a
// language the host has no built-in adapter for. Root is the project
// root the commands will run in.
type FreezeParams struct {
	Language string `json:"language"`
	Root     string `json:"root"`
}

// FreezeResult lists the commands the host should run, in order. Each
// step is one argv. The host validates every argument (non-empty,
// NUL-free) and runs the commands itself under its own policy; a plugin
// never executes anything.
type FreezeResult struct {
	Steps   [][]string `json:"steps"`
	Profile string     `json:"profile,omitempty"`
}

// PlanParams asks a plugin for advisory notes to merge into the
// project plan explanation.
type PlanParams struct {
	Language string `json:"language"`
	Root     string `json:"root"`
}

// PlanResult carries advisory, human-readable notes. Notes never change
// what the host executes.
type PlanResult struct {
	Notes []string `json:"notes,omitempty"`
}

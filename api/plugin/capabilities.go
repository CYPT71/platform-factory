package plugin

import sdk "github.com/CYPT71/secure-oci-base/sdk/plugin"

// Re-export all standard capabilities from sdk/plugin for public API.
// See Sanetizer-todo.md items 11-12 for capability-based architecture.
const (
	// Language capabilities (backward compatible)
	CapabilityDetect = sdk.CapabilityDetect
	CapabilityFreeze = sdk.CapabilityFreeze
	CapabilityPlan   = sdk.CapabilityPlan
	CapabilityScan   = sdk.CapabilityScan

	// Runtime capabilities
	CapabilityRuntimeCreate = sdk.CapabilityRuntimeCreate
	CapabilityRuntimeStop   = sdk.CapabilityRuntimeStop
	CapabilityRuntimeLogs   = sdk.CapabilityRuntimeLogs
	CapabilityRuntimeStatus = sdk.CapabilityRuntimeStatus
	CapabilityRuntimeExec   = sdk.CapabilityRuntimeExec

	// Deployment capabilities
	CapabilityDeploymentPlan     = sdk.CapabilityDeploymentPlan
	CapabilityDeploymentApply    = sdk.CapabilityDeploymentApply
	CapabilityDeploymentObserve  = sdk.CapabilityDeploymentObserve
	CapabilityDeploymentRollback = sdk.CapabilityDeploymentRollback
	CapabilityDeploymentDelete   = sdk.CapabilityDeploymentDelete

	// Builder capabilities
	CapabilityBuilderBuild = sdk.CapabilityBuilderBuild
	CapabilityBuilderTest  = sdk.CapabilityBuilderTest
	CapabilityBuilderClean = sdk.CapabilityBuilderClean
	CapabilityBuilderPush  = sdk.CapabilityBuilderPush

	// Analyzer capabilities
	CapabilityAnalyzerScan   = sdk.CapabilityAnalyzerScan
	CapabilityAnalyzerAttest = sdk.CapabilityAnalyzerAttest
	CapabilityAnalyzerVerify = sdk.CapabilityAnalyzerVerify
	CapabilityAnalyzerSign   = sdk.CapabilityAnalyzerSign

	// Registry capabilities
	CapabilityRegistryPush   = sdk.CapabilityRegistryPush
	CapabilityRegistryPull   = sdk.CapabilityRegistryPull
	CapabilityRegistryList   = sdk.CapabilityRegistryList
	CapabilityRegistryDelete = sdk.CapabilityRegistryDelete
)

type HelloResult = sdk.HelloResult
type DetectParams = sdk.DetectParams
type DetectResult = sdk.DetectResult
type FreezeParams = sdk.FreezeParams
type FreezeResult = sdk.FreezeResult
type PlanParams = sdk.PlanParams
type PlanResult = sdk.PlanResult

// ValidateCapability is re-exported from sdk/plugin for public API.
func ValidateCapability(cap string) error {
	return sdk.ValidateCapability(cap)
}

// HasCapability is re-exported from sdk/plugin for public API.
// Sanetizer-todo item 12: Capability negotiation.
func HasCapability(capabilities []string, cap string) bool {
	return sdk.HasCapability(capabilities, cap)
}

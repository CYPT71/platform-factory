package v1

import sdk "github.com/CYPT71/platform-factory/sdk/plugin"

const (
	// Language capabilities.
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

func ValidateCapability(cap string) error {
	return sdk.ValidateCapability(cap)
}

func HasCapability(capabilities []string, cap string) bool {
	return sdk.HasCapability(capabilities, cap)
}

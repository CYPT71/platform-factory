// Package pipeline re-exports scheduler types for backward compatibility.
// New code should import "github.com/CYPT71/platform-factory/internal/scheduler" directly.
package pipeline

import (
	"github.com/CYPT71/platform-factory/internal/scheduler"
)

// Re-export types from scheduler package for backward compatibility

// StageRunner is an alias for scheduler.StageRunner.
type StageRunner = scheduler.StageRunner

// StageRunnerFunc is an alias for scheduler.StageRunnerFunc.
type StageRunnerFunc = scheduler.StageRunnerFunc

// StageState is an alias for scheduler.StageState.
type StageState = scheduler.StageState

// StageSucceeded is an alias for scheduler.StageSucceeded.
const StageSucceeded StageState = scheduler.StageSucceeded

// StageFailed is an alias for scheduler.StageFailed.
const StageFailed StageState = scheduler.StageFailed

// StageBlocked is an alias for scheduler.StageBlocked.
const StageBlocked StageState = scheduler.StageBlocked

// StageCanceled is an alias for scheduler.StageCanceled.
const StageCanceled StageState = scheduler.StageCanceled

// StageBudgetExceeded is an alias for scheduler.StageBudgetExceeded.
const StageBudgetExceeded StageState = scheduler.StageBudgetExceeded

// StageResult is an alias for scheduler.StageResult.
type StageResult = scheduler.StageResult

// ScheduleResult is an alias for scheduler.ScheduleResult.
type ScheduleResult = scheduler.ScheduleResult

// ScheduleError is an alias for scheduler.ScheduleError.
type ScheduleError = scheduler.ScheduleError

// ScheduleBudgetExceededError is an alias for scheduler.ScheduleBudgetExceededError.
type ScheduleBudgetExceededError = scheduler.ScheduleBudgetExceededError

// Scheduler is an alias for scheduler.Scheduler.
type Scheduler = scheduler.Scheduler

// Graph is an alias for scheduler.Graph.
type Graph = scheduler.Graph

// Issue is an alias for scheduler.Issue.
type Issue = scheduler.Issue

// ValidationError is an alias for scheduler.ValidationError.
type ValidationError = scheduler.ValidationError

// Analyze is an alias for scheduler.Analyze.
var Analyze = scheduler.Analyze

// MaxStages is an alias for scheduler.MaxStages.
const MaxStages = scheduler.MaxStages

// IDPattern is an alias for scheduler.IDPattern.
var IDPattern = scheduler.IDPattern

// Capability constants
const (
	CapabilityArtifacts      = scheduler.CapabilityArtifacts
	CapabilityCache          = scheduler.CapabilityCache
	CapabilityMemoryRlimit   = scheduler.CapabilityMemoryRlimit
	CapabilityNetworkNone    = scheduler.CapabilityNetworkNone
	CapabilityParallelStages = scheduler.CapabilityParallelStages
	CapabilitySecrets        = scheduler.CapabilitySecrets
	CapabilitySandbox        = scheduler.CapabilitySandbox
	CapabilityCgroupCPU      = scheduler.CapabilityCgroupCPU
	CapabilityCgroupPIDs     = scheduler.CapabilityCgroupPIDs
)

// KnownCapabilities returns every capability name this engine can negotiate, sorted.
func KnownCapabilities() []string {
	return scheduler.KnownCapabilities()
}

// Note: Methods are inherited from the scheduler package types.
// The type aliases above automatically inherit all methods from their
// underlying scheduler types.

// Internal helper functions

// validateRequiredCapabilities is used internally by graph validation.
func validateRequiredCapabilities(names []string) []Issue {
	return scheduler.ValidateRequiredCapabilities(names)
}

// newValidationError is used internally.
var newValidationError = scheduler.NewValidationError

// maxStages alias for internal use.
const maxStages = MaxStages

// idPattern alias for internal use.
var idPattern = IDPattern

// validDigest is re-exported for tests.
var validDigest = scheduler.ValidDigest

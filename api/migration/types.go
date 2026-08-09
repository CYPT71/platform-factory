// Package migration defines the canonical models and data structures for Platform Factory's migration system.
//
// This package provides the core abstractions used throughout the migration orchestration process,
// including resource definitions, dependency graphs, migration plans and related validation logic.
package migration

// FormatVersion is the current canonical migration document format.
const FormatVersion = "v1"

// DiscoveryStatus represents the status of a discovery operation
type DiscoveryStatus string

const (
	// DiscoveryStatusComplete indicates that discovery completed successfully with no errors or omissions
	DiscoveryStatusComplete DiscoveryStatus = "complete"

	// DiscoveryStatusPartial indicates that discovery was partially successful, with some resources missing
	DiscoveryStatusPartial DiscoveryStatus = "partial"

	// DiscoveryStatusFailed indicates that discovery failed entirely
	DiscoveryStatusFailed DiscoveryStatus = "failed"
)

// Compatibility describes whether a canonical resource can be represented by
// a target capability. It deliberately contains no implementation identity.
type Compatibility string

const (
	CompatibilityDirect      Compatibility = "direct"
	CompatibilityAdaptable   Compatibility = "adaptable"
	CompatibilityDegraded    Compatibility = "degraded"
	CompatibilityUnsupported Compatibility = "unsupported"
)

func (c Compatibility) valid() bool {
	switch c {
	case CompatibilityDirect, CompatibilityAdaptable, CompatibilityDegraded, CompatibilityUnsupported:
		return true
	default:
		return false
	}
}

func (s DiscoveryStatus) valid() bool {
	switch s {
	case DiscoveryStatusComplete, DiscoveryStatusPartial, DiscoveryStatusFailed:
		return true
	default:
		return false
	}
}

// Package migration provides public helpers for constructing and verifying
// canonical migration documents without depending on host internals.
package migration

import apimigration "github.com/CYPT71/platform-factory/api/migration/v1"

// NewPlan creates an unsealed canonical plan. Call Seal after populating it.
func NewPlan(status apimigration.DiscoveryStatus) apimigration.MigrationPlan {
	return apimigration.MigrationPlan{Version: apimigration.FormatVersion, DiscoveryStatus: status}
}

// Seal computes and installs the canonical content digest.
func Seal(plan *apimigration.MigrationPlan) error { return plan.SetDigest() }

// Verify validates both the semantic content and its canonical digest.
func Verify(plan *apimigration.MigrationPlan) error { return plan.Validate() }

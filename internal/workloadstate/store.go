// Package workloadstate is the durable counterpart to
// internal/core.RuntimeState/TransitionTo: the state machine itself is a
// pure, in-memory transition validator with no persistence opinion (see
// statemachine.go's own package doc), which is correct for a library but
// leaves nothing for a CLI command - whose process exits between every
// invocation - to read a workload's last known Phase back from before
// calling TransitionTo again. Store is that missing piece, kept as small
// as the state machine itself: a plain key/value record of the last
// observed RuntimeState per WorkloadID, with no transition logic of its
// own - exactly the same separation internal/core.OperationJournal keeps
// from internal/plugin.Client.CallWithIdempotency's idempotency logic.
package workloadstate

import "github.com/CYPT71/platform-factory/internal/core"

// Store persists one RuntimeState per WorkloadID.
type Store interface {
	// Lookup returns the last saved state for id, if any was ever saved.
	Lookup(id core.WorkloadID) (core.RuntimeState, bool, error)
	// Save durably records state as id's current RuntimeState, overwriting
	// whatever was saved before it.
	Save(id core.WorkloadID, state core.RuntimeState) error
}

// ValidWorkloadID rejects values that cannot safely name a durable record
// - the same character-class rule core.ValidOperationID already applies to
// OperationID, applied here to WorkloadID for the same reason: a record
// name derived from an ID must never be usable as a path traversal or
// contain characters a filesystem would reject.
func ValidWorkloadID(id core.WorkloadID) bool {
	return core.ValidOperationID(core.OperationID(id))
}

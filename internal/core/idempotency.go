package core

import "errors"

// OperationStatus is the durable state of a logical mutation.
type OperationStatus string

const (
	OperationStarted   OperationStatus = "started"
	OperationCompleted OperationStatus = "completed"
	OperationFailed    OperationStatus = "failed"
)

// OperationRecord contains only replay-safe identity and state. Plugin output
// and provider errors are deliberately excluded because both may contain
// secrets or other untrusted data.
type OperationRecord struct {
	ID     OperationID
	Status OperationStatus
	Scope  string
}

// OperationJournal is the single idempotency persistence port. Start is an
// atomic claim: exactly one caller may receive started=true for an ID/scope.
type OperationJournal interface {
	Lookup(OperationID) (OperationRecord, bool)
	Start(OperationID, string) (bool, error)
	Complete(OperationID) error
	Fail(OperationID) error
}

// ErrOperationIndeterminate means a mutation may have happened but no durable
// terminal observation exists. Callers must observe external state before any
// retry.
var ErrOperationIndeterminate = errors.New("operation outcome is indeterminate; re-observe external state")

// ValidOperationID rejects values that cannot safely identify a journal
// record. The same validation applies to every journal adapter.
func ValidOperationID(id OperationID) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if r < 32 || r > 126 || r == '/' || r == '\\' || r == ':' {
			return false
		}
	}
	return true
}

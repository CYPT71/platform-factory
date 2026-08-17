package idempotency

import (
	"fmt"
	"sync"

	"github.com/CYPT71/platform-factory/internal/core"
)

// MemoryJournal implements the canonical journal contract for tests and
// process-local workflows. It intentionally makes no crash-safety claim.
type MemoryJournal struct {
	mu      sync.Mutex
	records map[core.OperationID]core.OperationRecord
}

func NewMemoryJournal() *MemoryJournal {
	return &MemoryJournal{records: make(map[core.OperationID]core.OperationRecord)}
}

func (j *MemoryJournal) Lookup(id core.OperationID) (core.OperationRecord, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, ok := j.records[id]
	return record, ok
}

func (j *MemoryJournal) Start(id core.OperationID, scope string) (bool, error) {
	if !core.ValidOperationID(id) || scope == "" {
		return false, fmt.Errorf("operation id and scope are required")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if existing, ok := j.records[id]; ok {
		if existing.Scope != scope {
			return false, fmt.Errorf("operation id %q collides with a different operation scope", id)
		}
		return false, nil
	}
	j.records[id] = core.OperationRecord{ID: id, Scope: scope, Status: core.OperationStarted}
	return true, nil
}

func (j *MemoryJournal) Complete(id core.OperationID) error {
	return j.finish(id, core.OperationCompleted)
}

func (j *MemoryJournal) Fail(id core.OperationID) error {
	return j.finish(id, core.OperationFailed)
}

func (j *MemoryJournal) finish(id core.OperationID, status core.OperationStatus) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, ok := j.records[id]
	if !ok {
		return fmt.Errorf("operation %q was never started", id)
	}
	if record.Status != core.OperationStarted {
		return fmt.Errorf("operation %q already finished with status %q", id, record.Status)
	}
	record.Status = status
	j.records[id] = record
	return nil
}

var _ core.OperationJournal = (*MemoryJournal)(nil)

package workloadstate

import (
	"fmt"
	"sync"

	"github.com/CYPT71/platform-factory/internal/core"
)

// MemoryStore implements Store for tests and process-local workflows. It
// intentionally makes no crash-safety claim - the same tradeoff
// internal/idempotency.MemoryJournal documents for the operation journal.
type MemoryStore struct {
	mu     sync.Mutex
	states map[core.WorkloadID]core.RuntimeState
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{states: make(map[core.WorkloadID]core.RuntimeState)}
}

func (s *MemoryStore) Lookup(id core.WorkloadID) (core.RuntimeState, bool, error) {
	if !ValidWorkloadID(id) {
		return core.RuntimeState{}, false, fmt.Errorf("workload id %q is invalid", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[id]
	return state, ok, nil
}

func (s *MemoryStore) Save(id core.WorkloadID, state core.RuntimeState) error {
	if !ValidWorkloadID(id) {
		return fmt.Errorf("workload id %q is invalid", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[id] = state
	return nil
}

var _ Store = (*MemoryStore)(nil)

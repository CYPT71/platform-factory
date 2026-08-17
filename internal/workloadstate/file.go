package workloadstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/CYPT71/platform-factory/internal/atomicfile"
	"github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/strictjson"
)

// FileStore is a Store backed by one JSON file per workload under root,
// named directly by the (validated, filesystem-safe) WorkloadID - the same
// per-record-file shape internal/idempotency.FileJournal uses for
// operation records. Unlike an operation record, a workload's RuntimeState
// is not write-once: Save legitimately overwrites the previous phase on
// every transition, so this uses a plain atomic rename rather than
// FileJournal's hard-link compare-and-set claim (which exists specifically
// to make the *first* write racy-safe across concurrent starters - a
// workload's state store has no equivalent "only one caller may create
// this" requirement).
type FileStore struct {
	mu   sync.Mutex
	root string
}

// NewFileStore creates or opens a persistent workload-state store at root.
// Root must be explicit so composition owns persistence placement, the
// same requirement NewFileJournal already documents for the operation
// journal.
func NewFileStore(root string) (*FileStore, error) {
	if root == "" {
		return nil, errors.New("workload state: root directory is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("workload state: create root directory: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("workload state: inspect root directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("workload state: root must be a real directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(root, 0o700); err != nil {
			return nil, fmt.Errorf("workload state: protect root directory: %w", err)
		}
	}
	return &FileStore{root: root}, nil
}

type persistedRuntimeState struct {
	Phase core.Phase `json:"phase"`
}

func (s *FileStore) path(id core.WorkloadID) string {
	return filepath.Join(s.root, string(id))
}

// Lookup reads id's last saved state. A missing file (never saved) is not
// an error: it reports found=false.
func (s *FileStore) Lookup(id core.WorkloadID) (core.RuntimeState, bool, error) {
	if !ValidWorkloadID(id) {
		return core.RuntimeState{}, false, fmt.Errorf("workload id %q is invalid", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.path(id)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return core.RuntimeState{}, false, nil
	}
	if err != nil {
		return core.RuntimeState{}, false, fmt.Errorf("workload state: inspect record: %w", err)
	}
	if !info.Mode().IsRegular() {
		return core.RuntimeState{}, false, errors.New("workload state: record is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return core.RuntimeState{}, false, fmt.Errorf("workload state: read record: %w", err)
	}
	stored, err := decodePersistedRuntimeState(data)
	if err != nil {
		return core.RuntimeState{}, false, fmt.Errorf("workload state: decode record: %w", err)
	}
	return core.RuntimeState{Phase: stored.Phase}, true, nil
}

// Save durably and atomically overwrites id's saved state: writes to a
// temp file in the same directory (so the final rename is on the same
// filesystem, keeping it atomic), fsyncs it, renames it into place, then
// fsyncs the directory - the same durability sequence FileJournal.persist
// uses for operation records.
func (s *FileStore) Save(id core.WorkloadID, state core.RuntimeState) error {
	if !ValidWorkloadID(id) {
		return fmt.Errorf("workload id %q is invalid", id)
	}
	stored := persistedRuntimeState{Phase: state.Phase}
	data, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("workload state: encode record: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := atomicfile.Write(s.root, string(id), data, 0o600, true); err != nil {
		return fmt.Errorf("workload state: persist record: %w", err)
	}
	return nil
}

func decodePersistedRuntimeState(data []byte) (persistedRuntimeState, error) {
	var stored persistedRuntimeState
	return stored, strictjson.Decode(data, &stored)
}

var _ Store = (*FileStore)(nil)

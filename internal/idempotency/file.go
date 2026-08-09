// Package plugin is the host side of the out-of-process plugin
// boundary: it launches plugin subprocesses, performs the handshake,
// verifies signed digest-pinned manifests and discovers installed
// plugins.
package idempotency

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/CYPT71/secure-oci-base/internal/core"
)

// FileJournal records the outcome of every mutating operation by
// OperationID and answers whether a given ID has already run.
// This persistent implementation survives process crashes between
// "operation started" and "operation confirmed", which is the exact
// scenario Sanetizer-todo item 13 lists as a required test case.
//
// It stores records in a dedicated directory with one file per operation,
// providing durability across process restarts and host crashes.
type FileJournal struct {
	mu      sync.Mutex
	root    string
	records map[core.OperationID]core.OperationRecord
}

type persistedOperationRecord struct {
	ID     core.OperationID     `json:"id"`
	Status core.OperationStatus `json:"status"`
	Scope  string               `json:"scope"`
}

// DefaultOperationJournalRoot is the default directory for the operation journal.
// Callers should normally pass an explicit root to NewFileJournal.
const DefaultOperationJournalRoot = "/var/lib/platform-factory/operation-journal"

// NewFileJournal creates a new persistent operation journal at the
// given root directory. If the directory does not exist, it will be created.
// Any existing operation records in the directory will be loaded on startup.
// Root must be explicit so composition owns persistence placement and policy.
func NewFileJournal(root string) (*FileJournal, error) {
	if root == "" {
		return nil, errors.New("operation journal: root directory is required")
	}

	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("operation journal: create root directory: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("operation journal: inspect root directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("operation journal: root must be a real directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(root, 0o700); err != nil {
			return nil, fmt.Errorf("operation journal: protect root directory: %w", err)
		}
	}

	journal := &FileJournal{
		root:    root,
		records: make(map[core.OperationID]core.OperationRecord),
	}

	// Load existing records from disk
	if err := journal.load(); err != nil {
		return nil, fmt.Errorf("operation journal: load existing records: %w", err)
	}

	return journal, nil
}

// load reads all operation records from the journal directory.
func (j *FileJournal) load() error {
	entries, err := os.ReadDir(j.root)
	if err != nil {
		return fmt.Errorf("read journal directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		// Record files are named as the operation ID
		if !isValidOperationFile(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect operation record %q: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("operation record %q is not a regular file", name)
		}

		data, err := os.ReadFile(filepath.Join(j.root, name))
		if err != nil {
			return fmt.Errorf("read operation record %q: %w", name, err)
		}

		stored, err := decodePersistedOperationRecord(data)
		if err != nil {
			return fmt.Errorf("decode operation record %q: %w", name, err)
		}
		record := core.OperationRecord{ID: stored.ID, Status: stored.Status, Scope: stored.Scope}
		// Only keep records that match their filename (sanity check)
		if record.ID != core.OperationID(name) {
			return fmt.Errorf("operation record %q contains mismatched id %q", name, record.ID)
		}
		if record.Status != core.OperationStarted && record.Status != core.OperationCompleted && record.Status != core.OperationFailed {
			return fmt.Errorf("operation record %q has invalid status %q", name, record.Status)
		}
		j.records[record.ID] = record
	}

	return nil
}

// isValidOperationFile returns true if the filename is a valid operation ID.
// Operation IDs are non-empty strings without path separators or control characters.
func isValidOperationFile(name string) bool {
	return core.ValidOperationID(core.OperationID(name))
}

// Lookup returns the recorded outcome for id, if any operation with that
// ID has ever started.
func (j *FileJournal) Lookup(id core.OperationID) (core.OperationRecord, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, err := j.loadRecord(id)
	if errors.Is(err, os.ErrNotExist) {
		return core.OperationRecord{}, false
	}
	if err != nil {
		// Lookup cannot return an error through the canonical port. Do not expose
		// a possibly stale in-memory terminal state when disk verification fails.
		return core.OperationRecord{}, false
	}
	j.records[id] = record
	return record, true
}

// Start records id as started, and reports whether this call actually
// started it (false means id was already recorded - started, completed,
// or failed - and the caller must not begin the operation a second
// time). This is the compare-and-set the "duplicate submission" test
// case in Sanetizer-todo item 13 needs: two concurrent Start calls with the
// same id can never both return true.
func (j *FileJournal) Start(id core.OperationID, scope string) (started bool, err error) {
	if !isValidOperationFile(string(id)) {
		return false, fmt.Errorf("invalid operation id %q", id)
	}
	if scope == "" {
		return false, errors.New("operation scope is required")
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	if existing, exists := j.records[id]; exists {
		if existing.Scope != scope {
			return false, fmt.Errorf("operation id %q collides with a different operation scope", id)
		}
		return false, nil
	}

	record := core.OperationRecord{ID: id, Status: core.OperationStarted, Scope: scope}
	if err := j.claim(record); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, loadErr := j.loadRecord(id)
			if loadErr != nil {
				return false, fmt.Errorf("load existing operation claim: %w", loadErr)
			}
			j.records[id] = existing
			if existing.Scope != scope {
				return false, fmt.Errorf("operation id %q collides with a different operation scope", id)
			}
			return false, nil
		}
		return false, fmt.Errorf("persist operation start: %w", err)
	}
	j.records[id] = record
	return true, nil
}

// Complete records id as completed with the given result. It is an
// error to complete an id that was never started, or to complete one
// twice - both indicate a caller bug (a journal entry's terminal state
// must be written exactly once), not a condition to silently overwrite.
func (j *FileJournal) Complete(id core.OperationID) error {
	// Plugin responses are untrusted and may contain secrets. Until callers can
	// provide a result explicitly classified as replay-safe, persist only the
	// terminal status. A duplicate requiring a result fails explicitly.
	return j.finish(id, core.OperationRecord{ID: id, Status: core.OperationCompleted})
}

// Fail records id as failed with the given error, under the same
// write-once rule Complete follows.
func (j *FileJournal) Fail(id core.OperationID) error {
	// Never persist an external error message: it may embed credentials or raw
	// provider data. The durable record preserves only the terminal class.
	return j.finish(id, core.OperationRecord{ID: id, Status: core.OperationFailed})
}

// finish is the internal implementation of Complete and Fail.
func (j *FileJournal) finish(id core.OperationID, terminal core.OperationRecord) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	existing, err := j.loadRecord(id)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("operation %q was never started", id)
	}
	if err != nil {
		return fmt.Errorf("load operation %q before terminal transition: %w", id, err)
	}
	if existing.Status != core.OperationStarted {
		return fmt.Errorf("operation %q already finished with status %q", id, existing.Status)
	}
	terminal.Scope = existing.Scope

	if err := j.persist(terminal); err != nil {
		return fmt.Errorf("persist terminal operation state: %w", err)
	}
	j.records[id] = terminal
	return nil
}

// persist writes a single operation record to disk atomically.
func (j *FileJournal) persist(record core.OperationRecord) error {
	stored := persistedOperationRecord{ID: record.ID, Status: record.Status, Scope: record.Scope}
	data, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("marshal operation record: %w", err)
	}

	// CreateTemp uses O_EXCL and an unpredictable name, preventing symlink and
	// predictable-temp-file attacks by another local process.
	tmpFile, err := os.CreateTemp(j.root, ".operation-*")
	if err != nil {
		return fmt.Errorf("write temp operation record: %w", err)
	}
	tmpPath := tmpFile.Name()
	finalPath := filepath.Join(j.root, string(record.ID))
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("protect temp operation record: %w", err)
	}
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp operation record: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync temp operation record: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp operation record: %w", err)
	}

	// Rename is atomic on POSIX systems
	if err := os.Rename(tmpPath, finalPath); err != nil {
		// Clean up temp file on failure
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename operation record: %w", err)
	}

	// Sync the directory to ensure the rename is durable
	// Note: This doesn't guarantee durability on all filesystems,
	// but it's better than nothing for 10+ year robustness.
	dirFile, err := os.Open(j.root)
	if err != nil {
		return fmt.Errorf("open journal directory for sync: %w", err)
	}
	if err := dirFile.Sync(); err != nil {
		_ = dirFile.Close()
		return fmt.Errorf("sync journal directory: %w", err)
	}
	if err := dirFile.Close(); err != nil {
		return fmt.Errorf("close journal directory: %w", err)
	}

	return nil
}

// claim publishes a fully written durable record with an atomic hard-link. The
// link is the compare-and-set authority: independent instances cannot both
// create the final name, and losers can never observe a partially written file.
func (j *FileJournal) claim(record core.OperationRecord) error {
	stored := persistedOperationRecord{ID: record.ID, Status: record.Status, Scope: record.Scope}
	data, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("marshal operation claim: %w", err)
	}
	file, err := os.CreateTemp(j.root, ".claim-*")
	if err != nil {
		return err
	}
	tmpPath := file.Name()
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	path := filepath.Join(j.root, string(record.ID))
	if err := os.Link(tmpPath, path); err != nil {
		return err
	}
	if err := os.Remove(tmpPath); err != nil {
		return err
	}
	cleanup = false
	return syncDirectory(j.root)
}

func (j *FileJournal) loadRecord(id core.OperationID) (core.OperationRecord, error) {
	path := filepath.Join(j.root, string(id))
	info, err := os.Lstat(path)
	if err != nil {
		return core.OperationRecord{}, err
	}
	if !info.Mode().IsRegular() {
		return core.OperationRecord{}, errors.New("operation claim is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return core.OperationRecord{}, err
	}
	stored, err := decodePersistedOperationRecord(data)
	if err != nil {
		return core.OperationRecord{}, err
	}
	if stored.ID != id || stored.Scope == "" {
		return core.OperationRecord{}, errors.New("operation claim identity mismatch")
	}
	if stored.Status != core.OperationStarted && stored.Status != core.OperationCompleted && stored.Status != core.OperationFailed {
		return core.OperationRecord{}, errors.New("operation claim has invalid status")
	}
	record := core.OperationRecord{ID: id, Status: stored.Status, Scope: stored.Scope}
	return record, nil
}

func decodePersistedOperationRecord(data []byte) (persistedOperationRecord, error) {
	var stored persistedOperationRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields() // fail closed on legacy raw result/error fields
	if err := decoder.Decode(&stored); err != nil {
		return persistedOperationRecord{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return persistedOperationRecord{}, errors.New("trailing operation record data")
		}
		return persistedOperationRecord{}, err
	}
	return stored, nil
}

func syncDirectory(root string) error {
	dirFile, err := os.Open(root)
	if err != nil {
		return err
	}
	defer dirFile.Close()
	return dirFile.Sync()
}

// Close releases resources held by the journal. Currently a no-op,
// but provided for interface symmetry.
func (j *FileJournal) Close() error {
	return nil
}

var _ core.OperationJournal = (*FileJournal)(nil)

package idempotency

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/CYPT71/platform-factory/internal/core"
)

func TestNewFileJournalRejectsEmptyRoot(t *testing.T) {
	_, err := NewFileJournal("")
	if err == nil || !strings.Contains(err.Error(), "root directory") {
		t.Fatalf("expected error for empty root, got: %v", err)
	}
}

func TestNewFileJournalCreatesDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operation-journal")
	if _, err := os.Stat(root); err == nil {
		t.Fatal("journal directory should not exist yet")
	}

	journal, err := NewFileJournal(root)
	if err != nil {
		t.Fatalf("NewFileJournal: %v", err)
	}
	defer journal.Close()

	if _, err := os.Stat(root); err != nil {
		t.Fatalf("journal directory should exist: %v", err)
	}
}

func TestNewFileJournalRejectsNonDirectoryRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileJournal(root); err == nil {
		t.Fatal("regular file accepted as journal root")
	}
}

func TestNewFileJournalRejectsRootBelowRegularFile(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileJournal(filepath.Join(parent, "journal")); err == nil {
		t.Fatal("journal root below regular file was created")
	}
}

func TestFileJournalLookupEmpty(t *testing.T) {
	journal, err := NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileJournal: %v", err)
	}
	defer journal.Close()

	_, exists := journal.Lookup("nonexistent")
	if exists {
		t.Fatal("expected no record for nonexistent operation")
	}
}

func TestFileJournalLoadReportsVanishedRoot(t *testing.T) {
	root := t.TempDir()
	journal, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := journal.load(); err == nil {
		t.Fatal("load succeeded after journal root vanished")
	}
}

func TestFileJournalLoadRejectsUnreadableRecord(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "unreadable")
	if err := os.WriteFile(path, []byte(`{"id":"unreadable","status":"started","scope":"scope"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Skipf("cannot remove record permissions: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, err := NewFileJournal(root); err == nil {
		t.Skip("filesystem/user permits reads despite mode 000")
	}
}

func TestFileJournalStartAndLookup(t *testing.T) {
	journal, err := NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileJournal: %v", err)
	}
	defer journal.Close()

	opID := core.OperationID("test-op-1")

	// Start should return true for new operation
	if started, err := journal.Start(opID, "scope"); err != nil || !started {
		t.Fatal("expected Start to return true for new operation")
	}

	// Lookup should find the started operation
	record, exists := journal.Lookup(opID)
	if !exists {
		t.Fatal("expected to find operation after Start")
	}
	if record.ID != opID {
		t.Fatalf("expected ID %q, got %q", opID, record.ID)
	}
	if record.Status != core.OperationStarted {
		t.Fatalf("expected status %q, got %q", core.OperationStarted, record.Status)
	}
}

func TestFileJournalStartIdempotent(t *testing.T) {
	journal, err := NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileJournal: %v", err)
	}
	defer journal.Close()

	opID := core.OperationID("test-op-2")

	// First Start should return true
	if started, err := journal.Start(opID, "scope"); err != nil || !started {
		t.Fatal("expected first Start to return true")
	}

	// Second Start with same ID should return false
	if started, err := journal.Start(opID, "scope"); err != nil || started {
		t.Fatal("expected second Start to return false")
	}
}

func TestFileJournalComplete(t *testing.T) {
	journal, err := NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileJournal: %v", err)
	}
	defer journal.Close()

	opID := core.OperationID("test-op-3")

	// Must Start before Complete
	if started, err := journal.Start(opID, "scope"); err != nil || !started {
		t.Fatal("Start failed")
	}

	// Complete the operation
	if err := journal.Complete(opID); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Lookup should show completed status
	record, exists := journal.Lookup(opID)
	if !exists {
		t.Fatal("expected to find operation after Complete")
	}
	if record.Status != core.OperationCompleted {
		t.Fatalf("expected status %q, got %q", core.OperationCompleted, record.Status)
	}
}

func TestFileJournalCompleteWithoutStart(t *testing.T) {
	journal, err := NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileJournal: %v", err)
	}
	defer journal.Close()

	opID := core.OperationID("test-op-4")

	// Complete without Start should fail
	if err := journal.Complete(opID); err == nil {
		t.Fatal("expected error for Complete without Start")
	}
}

func TestFileJournalCompleteTwice(t *testing.T) {
	journal, err := NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileJournal: %v", err)
	}
	defer journal.Close()

	opID := core.OperationID("test-op-5")

	if started, err := journal.Start(opID, "scope"); err != nil || !started {
		t.Fatal("Start failed")
	}
	if err := journal.Complete(opID); err != nil {
		t.Fatalf("first Complete: %v", err)
	}

	// Second Complete should fail
	if err := journal.Complete(opID); err == nil {
		t.Fatal("expected error for second Complete")
	}
}

func TestFileJournalFail(t *testing.T) {
	journal, err := NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileJournal: %v", err)
	}
	defer journal.Close()

	opID := core.OperationID("test-op-6")

	if started, err := journal.Start(opID, "scope"); err != nil || !started {
		t.Fatal("Start failed")
	}

	if err := journal.Fail(opID); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	record, exists := journal.Lookup(opID)
	if !exists {
		t.Fatal("expected to find operation after Fail")
	}
	if record.Status != core.OperationFailed {
		t.Fatalf("expected status %q, got %q", core.OperationFailed, record.Status)
	}
}

func TestFileJournalFailWithoutStart(t *testing.T) {
	journal, err := NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileJournal: %v", err)
	}
	defer journal.Close()

	opID := core.OperationID("test-op-7")

	// Fail without Start should fail
	if err := journal.Fail(opID); err == nil {
		t.Fatal("expected error for Fail without Start")
	}
}

func TestFileJournalRejectsMissingScope(t *testing.T) {
	journal, err := NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if started, err := journal.Start("missing-scope", ""); err == nil || started {
		t.Fatalf("Start = %v, %v; want missing scope error", started, err)
	}
}

func TestFileJournalPersistsAcrossInstances(t *testing.T) {
	root := filepath.Join(t.TempDir(), "persistent-journal")

	// Create journal and start an operation
	journal1, err := NewFileJournal(root)
	if err != nil {
		t.Fatalf("NewFileJournal: %v", err)
	}

	opID := core.OperationID("persistent-op")
	if started, err := journal1.Start(opID, "scope"); err != nil || !started {
		t.Fatal("Start failed")
	}
	if err := journal1.Complete(opID); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	journal1.Close()

	// Create a new journal instance at the same root
	journal2, err := NewFileJournal(root)
	if err != nil {
		t.Fatalf("NewFileJournal (second): %v", err)
	}
	defer journal2.Close()

	// Lookup should find the persisted operation
	record, exists := journal2.Lookup(opID)
	if !exists {
		t.Fatal("expected to find persisted operation")
	}
	if record.Status != core.OperationCompleted {
		t.Fatalf("expected status %q, got %q", core.OperationCompleted, record.Status)
	}
}

func TestFileJournalLoadFailsClosedOnCorruptRecord(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal-with-invalid-files")

	// Create directory and add invalid files
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Add a valid operation file
	validData := `{"id":"valid-op","status":"completed","result":"ok"}`
	if err := os.WriteFile(filepath.Join(root, "valid-op"), []byte(validData), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Add an invalid JSON file
	if err := os.WriteFile(filepath.Join(root, "invalid"), []byte("{invalid json}"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Add a file with invalid operation ID (contains path separator)
	if err := os.WriteFile(filepath.Join(root, "invalid/id"), []byte(`{"id":"x","status":"started"}`), 0o600); err != nil {
		// This might fail on some systems, but that's okay
		_ = err
	}

	if _, err := NewFileJournal(root); err == nil {
		t.Fatal("expected corrupt durable state to fail closed")
	}
}

func TestFileJournalLoadValidation(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		contents string
	}{
		{name: "mismatched id", filename: "record", contents: `{"id":"other","status":"started","scope":"scope"}`},
		{name: "invalid status", filename: "record", contents: `{"id":"record","status":"invented","scope":"scope"}`},
		{name: "unknown field", filename: "record", contents: `{"id":"record","status":"started","scope":"scope","result":"secret"}`},
		{name: "trailing document", filename: "record", contents: `{"id":"record","status":"started","scope":"scope"} {}`},
		{name: "malformed trailing data", filename: "record", contents: `{"id":"record","status":"started","scope":"scope"} {`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, tt.filename), []byte(tt.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewFileJournal(root); err == nil {
				t.Fatal("invalid durable record accepted")
			}
		})
	}
}

func TestFileJournalLoadIgnoresNonRecordsButRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "invalid:name"), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileJournal(root); err != nil {
		t.Fatalf("non-record entries should be ignored: %v", err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte(`{"id":"linked","status":"started","scope":"scope"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := NewFileJournal(root); err == nil {
		t.Fatal("symlink record accepted")
	}
}

func TestFileJournalProtectsExistingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	if err := os.Mkdir(root, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileJournal(root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("root permissions = %o", info.Mode().Perm())
	}
}

func TestFileJournalRejectsInvalidIDBeforePersistence(t *testing.T) {
	journal, err := NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if started, err := journal.Start("../escape", "scope"); err == nil || started {
		t.Fatalf("Start = %v, %v; want invalid ID error", started, err)
	}
	if _, exists := journal.Lookup("../escape"); exists {
		t.Fatal("invalid ID entered memory journal")
	}
}

func TestFileJournalStartPersistenceFailureLeavesNoMemoryRecord(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "journal")
	journal, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(base, "journal-backup")
	if err := os.Rename(root, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if started, err := journal.Start("persist-fails", "scope"); err == nil || started {
		t.Fatalf("Start = %v, %v; want persistence error", started, err)
	}
	if _, exists := journal.Lookup("persist-fails"); exists {
		t.Fatal("failed durable start entered memory journal")
	}
}

func TestFileJournalTerminalPersistenceFailureKeepsStartedState(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "journal")
	journal, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if started, err := journal.Start("terminal-fails", "scope"); err != nil || !started {
		t.Fatalf("Start = %v, %v", started, err)
	}
	backup := filepath.Join(base, "journal-backup")
	if err := os.Rename(root, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := journal.Complete("terminal-fails"); err == nil {
		t.Fatal("expected terminal persistence error")
	}
	if _, exists := journal.Lookup("terminal-fails"); exists {
		t.Fatal("corrupt journal root exposed stale in-memory state")
	}
	restarted, err := NewFileJournal(backup)
	if err != nil {
		t.Fatal(err)
	}
	record, exists := restarted.Lookup("terminal-fails")
	if !exists || record.Status != core.OperationStarted {
		t.Fatalf("record = %#v, %v; want durable started state", record, exists)
	}
}

func TestFileJournalTerminalTransitionFailsClosedOnCorruptRecord(t *testing.T) {
	root := t.TempDir()
	journal, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if started, err := journal.Start("corrupt-terminal", "scope"); err != nil || !started {
		t.Fatalf("Start = %v, %v", started, err)
	}
	if err := os.WriteFile(filepath.Join(root, "corrupt-terminal"), []byte(`{"id":"corrupt-terminal","status":"invented","scope":"scope"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := journal.Complete("corrupt-terminal"); err == nil {
		t.Fatal("corrupt durable state accepted for terminal transition")
	}
}

func TestFileJournalConcurrentDuplicateStartsOnce(t *testing.T) {
	journal, err := NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const callers = 32
	var wg sync.WaitGroup
	results := make(chan bool, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started, err := journal.Start("concurrent", "scope")
			if err != nil {
				t.Errorf("Start: %v", err)
			}
			results <- started
		}()
	}
	wg.Wait()
	close(results)
	startedCount := 0
	for started := range results {
		if started {
			startedCount++
		}
	}
	if startedCount != 1 {
		t.Fatalf("started count = %d, want 1", startedCount)
	}
}

func TestFileJournalRestartPreservesIndeterminateStart(t *testing.T) {
	root := t.TempDir()
	journal, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if started, err := journal.Start("crashed", "scope"); err != nil || !started {
		t.Fatalf("Start = %v, %v", started, err)
	}
	restarted, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	record, exists := restarted.Lookup("crashed")
	if !exists || record.Status != core.OperationStarted {
		t.Fatalf("record = %#v, %v; want indeterminate started record", record, exists)
	}
	if started, err := restarted.Start("crashed", "scope"); err != nil || started {
		t.Fatalf("duplicate Start = %v, %v; want no blind retry", started, err)
	}
}

func TestFileJournalRejectsOperationIDCollisionAcrossRestart(t *testing.T) {
	root := t.TempDir()
	journal, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if started, err := journal.Start("collision", "first-scope"); err != nil || !started {
		t.Fatalf("Start = %v, %v", started, err)
	}
	restarted, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if started, err := restarted.Start("collision", "different-scope"); err == nil || started {
		t.Fatalf("collision Start = %v, %v; want collision error", started, err)
	}
}

func TestFileJournalFailureDoesNotPersistRawError(t *testing.T) {
	root := t.TempDir()
	journal, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if started, err := journal.Start("failed", "scope"); err != nil || !started {
		t.Fatalf("Start = %v, %v", started, err)
	}
	const sentinel = "SECRET-SENTINEL-stable-failure"
	if err := journal.Fail("failed"); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	record, exists := restarted.Lookup("failed")
	if !exists || record.Status != core.OperationFailed {
		t.Fatalf("record = %#v, %v; raw failure must not survive restart", record, exists)
	}
	data, err := os.ReadFile(filepath.Join(root, "failed"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), sentinel) {
		t.Fatal("secret sentinel persisted in journal")
	}
}

func TestFileJournalTwoInstancesClaimOnce(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	first, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, journal := range []*FileJournal{first, second} {
		wg.Add(1)
		go func(j *FileJournal) {
			defer wg.Done()
			started, err := j.Start("cross-process", "scope")
			results <- started
			errs <- err
		}(journal)
	}
	wg.Wait()
	close(results)
	close(errs)
	started := 0
	for result := range results {
		if result {
			started++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if started != 1 {
		t.Fatalf("started = %d, want exactly one durable claimant", started)
	}
}

func TestFileJournalLosingClaimFailsClosedOnCorruptWinner(t *testing.T) {
	root := t.TempDir()
	winner, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	loser, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if started, err := winner.Start("claimed", "scope"); err != nil || !started {
		t.Fatalf("winner Start = %v, %v", started, err)
	}
	if err := os.WriteFile(filepath.Join(root, "claimed"), []byte(`{"id":"claimed","status":"invented","scope":"scope"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if started, err := loser.Start("claimed", "scope"); err == nil || started {
		t.Fatalf("loser Start = %v, %v; want corrupt claim error", started, err)
	}
}

func TestFileJournalSecondInstanceObservesTerminalTransition(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	first, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if started, err := first.Start("shared", "scope"); err != nil || !started {
		t.Fatalf("Start = %v, %v", started, err)
	}
	if record, ok := second.Lookup("shared"); !ok || record.Status != core.OperationStarted {
		t.Fatalf("second initial record = %+v, %v", record, ok)
	}
	if err := first.Complete("shared"); err != nil {
		t.Fatal(err)
	}
	if record, ok := second.Lookup("shared"); !ok || record.Status != core.OperationCompleted {
		t.Fatalf("second terminal record = %+v, %v", record, ok)
	}
	if err := second.Fail("shared"); err == nil {
		t.Fatal("stale second instance overwrote terminal state")
	}
}

func TestFileJournalLookupFailsClosedWhenRecordChangesAfterOpen(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "mismatched identity", content: `{"id":"other","status":"started","scope":"scope"}`},
		{name: "empty scope", content: `{"id":"changed","status":"started","scope":""}`},
		{name: "invalid status", content: `{"id":"changed","status":"invented","scope":"scope"}`},
		{name: "unknown field", content: `{"id":"changed","status":"started","scope":"scope","secret":"sentinel"}`},
		{name: "trailing json", content: `{"id":"changed","status":"started","scope":"scope"} {}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			journal, err := NewFileJournal(root)
			if err != nil {
				t.Fatal(err)
			}
			if started, err := journal.Start("changed", "scope"); err != nil || !started {
				t.Fatalf("Start = %v, %v", started, err)
			}
			if err := os.WriteFile(filepath.Join(root, "changed"), []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if record, ok := journal.Lookup("changed"); ok {
				t.Fatalf("corrupt post-open record exposed: %+v", record)
			}
		})
	}
}

func TestFileJournalLookupRejectsPostOpenNonRegularRecord(t *testing.T) {
	root := t.TempDir()
	journal, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if started, err := journal.Start("changed", "scope"); err != nil || !started {
		t.Fatalf("Start = %v, %v", started, err)
	}
	if err := os.Remove(filepath.Join(root, "changed")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "changed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if record, ok := journal.Lookup("changed"); ok {
		t.Fatalf("non-regular record exposed: %+v", record)
	}
}

func TestFileJournalPersistRenameFailureIsAtomic(t *testing.T) {
	root := t.TempDir()
	journal, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "blocked"), 0o700); err != nil {
		t.Fatal(err)
	}
	record := core.OperationRecord{ID: "blocked", Status: core.OperationCompleted, Scope: "scope"}
	if err := journal.persist(record); err == nil {
		t.Fatal("rename over directory unexpectedly succeeded")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".operation-") {
			t.Fatalf("temporary record leaked after rename failure: %s", entry.Name())
		}
	}
}

func TestFileJournalPersistFailsWhenRootBecomesNonDirectory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "journal")
	journal, err := NewFileJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(base, "journal-backup")
	if err := os.Rename(root, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := core.OperationRecord{ID: "terminal", Status: core.OperationCompleted, Scope: "scope"}
	if err := journal.persist(record); err == nil {
		t.Fatal("persistence into non-directory root succeeded")
	}
}

func TestSyncDirectoryRejectsMissingPath(t *testing.T) {
	if err := syncDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("sync of missing directory succeeded")
	}
}

func TestFileJournalRejectsSymlinkRootAndRecord(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(base, "link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := NewFileJournal(linkRoot); err == nil {
		t.Fatal("symlink journal root accepted")
	}
	if err := os.Symlink(filepath.Join(base, "target"), filepath.Join(realRoot, "operation")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileJournal(realRoot); err == nil {
		t.Fatal("symlink operation record accepted")
	}
}

func TestIsValidOperationFile(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"simple", true},
		{"with-dashes", true},
		{"with_underscores", true},
		{"with.dots", true},
		{"with123numbers", true},
		{"", false},
		{"/path/separator", false},
		{"\\windows\\path", false},
		{":colon", false},
		{"with\x00null", false},
		{"with control\x01", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isValidOperationFile(tt.name); got != tt.valid {
				t.Errorf("isValidOperationFile(%q) = %v, want %v", tt.name, got, tt.valid)
			}
		})
	}
}

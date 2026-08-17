package idempotency

import (
	"sync"
	"testing"

	"github.com/CYPT71/platform-factory/internal/core"
)

func testJournalContract(t *testing.T, newJournal func(*testing.T) core.OperationJournal) {
	t.Helper()
	t.Run("lifecycle", func(t *testing.T) {
		journal := newJournal(t)
		started, err := journal.Start("op-1", "scope-1")
		if err != nil || !started {
			t.Fatalf("first Start = %v, %v", started, err)
		}
		if err := journal.Complete("op-1"); err != nil {
			t.Fatal(err)
		}
		record, ok := journal.Lookup("op-1")
		if !ok || record.Status != core.OperationCompleted || record.Scope != "scope-1" {
			t.Fatalf("record = %+v, %v", record, ok)
		}
		if started, err := journal.Start("op-1", "scope-1"); err != nil || started {
			t.Fatalf("duplicate Start = %v, %v", started, err)
		}
		if started, err := journal.Start("op-1", "different"); err == nil || started {
			t.Fatalf("scope collision = %v, %v", started, err)
		}
	})
	t.Run("concurrent claim", func(t *testing.T) {
		journal := newJournal(t)
		const attempts = 32
		var wg sync.WaitGroup
		wins := make(chan bool, attempts)
		for range attempts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				started, err := journal.Start("op-race", "scope")
				if err != nil {
					t.Errorf("Start: %v", err)
				}
				wins <- started
			}()
		}
		wg.Wait()
		close(wins)
		count := 0
		for won := range wins {
			if won {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("wins = %d, want 1", count)
		}
	})
	t.Run("failure and invalid transitions", func(t *testing.T) {
		journal := newJournal(t)
		if started, err := journal.Start("", "scope"); err == nil || started {
			t.Fatalf("invalid ID Start = %v, %v", started, err)
		}
		if err := journal.Fail("missing"); err == nil {
			t.Fatal("Fail without Start succeeded")
		}
		if started, err := journal.Start("failed", "scope"); err != nil || !started {
			t.Fatalf("Start = %v, %v", started, err)
		}
		if err := journal.Fail("failed"); err != nil {
			t.Fatal(err)
		}
		if err := journal.Complete("failed"); err == nil {
			t.Fatal("terminal state was overwritten")
		}
	})
}

func TestMemoryJournalContract(t *testing.T) {
	testJournalContract(t, func(*testing.T) core.OperationJournal { return NewMemoryJournal() })
}

func TestFileJournalContract(t *testing.T) {
	testJournalContract(t, func(t *testing.T) core.OperationJournal {
		journal, err := NewFileJournal(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return journal
	})
}

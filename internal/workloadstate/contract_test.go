package workloadstate

import (
	"testing"

	"github.com/CYPT71/platform-factory/internal/core"
)

func testStoreContract(t *testing.T, newStore func(*testing.T) Store) {
	t.Helper()
	t.Run("lookup of a never-saved id reports not found, not an error", func(t *testing.T) {
		store := newStore(t)
		state, ok, err := store.Lookup("never-saved")
		if err != nil || ok || state.Phase != "" {
			t.Fatalf("state=%+v ok=%v err=%v", state, ok, err)
		}
	})
	t.Run("save then lookup round-trips the phase", func(t *testing.T) {
		store := newStore(t)
		if err := store.Save("wl-1", core.RuntimeState{Phase: core.PhaseBuilt}); err != nil {
			t.Fatal(err)
		}
		state, ok, err := store.Lookup("wl-1")
		if err != nil || !ok || state.Phase != core.PhaseBuilt {
			t.Fatalf("state=%+v ok=%v err=%v", state, ok, err)
		}
	})
	t.Run("save overwrites the previous phase", func(t *testing.T) {
		store := newStore(t)
		if err := store.Save("wl-2", core.RuntimeState{Phase: core.PhaseBuilt}); err != nil {
			t.Fatal(err)
		}
		if err := store.Save("wl-2", core.RuntimeState{Phase: core.PhasePublishing}); err != nil {
			t.Fatal(err)
		}
		state, ok, err := store.Lookup("wl-2")
		if err != nil || !ok || state.Phase != core.PhasePublishing {
			t.Fatalf("state=%+v ok=%v err=%v", state, ok, err)
		}
	})
	t.Run("rejects an invalid id", func(t *testing.T) {
		store := newStore(t)
		if err := store.Save("", core.RuntimeState{Phase: core.PhaseBuilt}); err == nil {
			t.Fatal("Save accepted an empty id")
		}
		if _, _, err := store.Lookup(""); err == nil {
			t.Fatal("Lookup accepted an empty id")
		}
		if err := store.Save("has/slash", core.RuntimeState{Phase: core.PhaseBuilt}); err == nil {
			t.Fatal("Save accepted an id containing a path separator")
		}
	})
	t.Run("distinct ids do not collide", func(t *testing.T) {
		store := newStore(t)
		if err := store.Save("wl-a", core.RuntimeState{Phase: core.PhaseBuilt}); err != nil {
			t.Fatal(err)
		}
		if err := store.Save("wl-b", core.RuntimeState{Phase: core.PhaseRunning}); err != nil {
			t.Fatal(err)
		}
		a, _, err := store.Lookup("wl-a")
		if err != nil || a.Phase != core.PhaseBuilt {
			t.Fatalf("wl-a=%+v err=%v", a, err)
		}
		b, _, err := store.Lookup("wl-b")
		if err != nil || b.Phase != core.PhaseRunning {
			t.Fatalf("wl-b=%+v err=%v", b, err)
		}
	})
}

func TestMemoryStoreContract(t *testing.T) {
	testStoreContract(t, func(*testing.T) Store { return NewMemoryStore() })
}

func TestFileStoreContract(t *testing.T) {
	testStoreContract(t, func(t *testing.T) Store {
		store, err := NewFileStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return store
	})
}

package core

import "testing"

var allPhases = []Phase{
	PhaseDeclared, PhasePlanned, PhaseBuilding, PhaseBuilt,
	PhasePublishing, PhasePublished, PhaseDeploying, PhaseRunning,
	PhaseStopping, PhaseStopped, PhaseDeleting, PhaseDeleted,
	PhaseFailed, PhaseUnknown,
}

func TestEveryPhaseCanDegradeToUnknown(t *testing.T) {
	for _, p := range allPhases {
		if p == PhaseUnknown {
			continue
		}
		if !CanTransition(p, PhaseUnknown) {
			t.Errorf("%q should be able to degrade to Unknown", p)
		}
	}
}

func TestTheHappyPathIsFullyConnected(t *testing.T) {
	path := []Phase{
		PhaseDeclared, PhasePlanned, PhaseBuilding, PhaseBuilt,
		PhasePublishing, PhasePublished, PhaseDeploying, PhaseRunning,
		PhaseStopping, PhaseStopped, PhaseDeleting, PhaseDeleted,
	}
	state := RuntimeState{}
	for _, next := range path {
		var err error
		state, err = state.TransitionTo(next)
		if err != nil {
			t.Fatalf("transition to %q failed: %v", next, err)
		}
		if state.Phase != next {
			t.Fatalf("state.Phase=%q, want %q", state.Phase, next)
		}
	}
}

func TestDeletingToDeletedIsIdempotent(t *testing.T) {
	rule, ok := LookupTransition(PhaseDeleting, PhaseDeleted)
	if !ok {
		t.Fatal("expected Deleting -> Deleted to be a valid transition")
	}
	if !rule.Idempotent {
		t.Fatal("Deleting -> Deleted must be idempotent - deleting an already-deleted resource is success, not an error")
	}
}

func TestPublishingToPublishedRequiresCompensation(t *testing.T) {
	rule, ok := LookupTransition(PhasePublishing, PhasePublished)
	if !ok {
		t.Fatal("expected Publishing -> Published to be a valid transition")
	}
	if rule.Idempotent {
		t.Fatal("Publishing -> Published must not be marked idempotent: a crash mid-upload leaves ambiguous state that needs re-verification, not a blind retry")
	}
	if rule.Compensation == "" {
		t.Fatal("expected a documented compensation for Publishing -> Published")
	}
}

func TestUnreachableTransitionsAreRejected(t *testing.T) {
	cases := []struct{ from, to Phase }{
		{PhaseDeclared, PhaseRunning},
		{PhaseRunning, PhaseDeclared},
		{PhaseDeleted, PhaseRunning},
		{PhaseBuilt, PhaseStopped},
	}
	for _, c := range cases {
		if CanTransition(c.from, c.to) {
			t.Errorf("expected %q -> %q to be rejected", c.from, c.to)
		}
	}
}

func TestTransitionToRejectsAnUnrecognizedTargetPhase(t *testing.T) {
	state := RuntimeState{Phase: PhaseDeclared}
	if _, err := state.TransitionTo(Phase("not-a-real-phase")); err == nil {
		t.Fatal("expected an error transitioning to an unrecognized phase")
	}
}

func TestTransitionToRejectsAnUnrecognizedCurrentPhase(t *testing.T) {
	state := RuntimeState{Phase: Phase("garbage")}
	if _, err := state.TransitionTo(PhasePlanned); err == nil {
		t.Fatal("expected an error transitioning from an unrecognized current phase")
	}
}

func TestTransitionToNeverMutatesTheReceiver(t *testing.T) {
	original := RuntimeState{Phase: PhaseDeclared}
	_, err := original.TransitionTo(PhasePlanned)
	if err != nil {
		t.Fatal(err)
	}
	if original.Phase != PhaseDeclared {
		t.Fatalf("original.Phase=%q, want it unchanged at Declared", original.Phase)
	}
}

func TestZeroValueRuntimeStateOnlyTransitionsToDeclared(t *testing.T) {
	var zero RuntimeState
	if _, err := zero.TransitionTo(PhasePlanned); err == nil {
		t.Fatal("a zero-value state has no prior phase; only -> Declared should be implicitly allowed")
	}
	next, err := zero.TransitionTo(PhaseDeclared)
	if err != nil || next.Phase != PhaseDeclared {
		t.Fatalf("next=%+v err=%v", next, err)
	}
}

func TestFailedCanRetryFromScratchOrBeDeleted(t *testing.T) {
	if !CanTransition(PhaseFailed, PhaseDeclared) {
		t.Error("Failed -> Declared (retry from scratch) should be allowed")
	}
	if !CanTransition(PhaseFailed, PhaseDeleting) {
		t.Error("Failed -> Deleting (give up and clean up) should be allowed")
	}
}

func TestUnknownCanRecoverToARepresentativeSetOfRealPhases(t *testing.T) {
	for _, to := range []Phase{PhaseRunning, PhaseDeleted, PhaseFailed, PhaseDeclared} {
		if !CanTransition(PhaseUnknown, to) {
			t.Errorf("Unknown -> %q should be allowed once the external system answers again", to)
		}
	}
}

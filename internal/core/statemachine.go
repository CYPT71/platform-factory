package core

import "fmt"

// Phase is the canonical workload state shared by every backend.
type Phase string

const (
	PhaseDeclared   Phase = "Declared"
	PhasePlanned    Phase = "Planned"
	PhaseBuilding   Phase = "Building"
	PhaseBuilt      Phase = "Built"
	PhasePublishing Phase = "Publishing"
	PhasePublished  Phase = "Published"
	PhaseDeploying  Phase = "Deploying"
	PhaseRunning    Phase = "Running"
	PhaseStopping   Phase = "Stopping"
	PhaseStopped    Phase = "Stopped"
	PhaseDeleting   Phase = "Deleting"
	PhaseDeleted    Phase = "Deleted"
	PhaseFailed     Phase = "Failed"
	// PhaseUnknown is not a terminal or working state - it means the
	// authoritative external system (containerd/KubeVirt/Kubernetes)
	// could not be reached to confirm what the real state is. A caller
	// observing Unknown must re-query rather than assume the last phase.
	PhaseUnknown Phase = "Unknown"
)

func (p Phase) valid() bool {
	switch p {
	case PhaseDeclared, PhasePlanned, PhaseBuilding, PhaseBuilt,
		PhasePublishing, PhasePublished, PhaseDeploying, PhaseRunning,
		PhaseStopping, PhaseStopped, PhaseDeleting, PhaseDeleted,
		PhaseFailed, PhaseUnknown:
		return true
	default:
		return false
	}
}

// TransitionRule documents one allowed state change: whether it's safe
// to repeat verbatim after a crash (Idempotent), and what a caller must
// do to compensate for a partial attempt before it may retry
// (Compensation, empty when no cleanup is needed).
type TransitionRule struct {
	From         Phase
	To           Phase
	Idempotent   bool
	Compensation string
}

// transitions is every move the canonical state machine allows. Any
// (From, To) pair not listed here is rejected by Transition - a
// backend adapter that thinks it needs a transition not in this table
// has found either a bug in its own translation or a real gap in this
// table, never something to route around.
var transitions = []TransitionRule{
	{PhaseDeclared, PhasePlanned, true, ""},
	{PhaseDeclared, PhaseFailed, true, ""},

	{PhasePlanned, PhaseBuilding, true, ""},
	{PhasePlanned, PhaseFailed, true, ""},

	{PhaseBuilding, PhaseBuilt, false,
		"a build that crashed mid-write may have left a partial artifact on disk or in the build cache; discard it and re-run Building from Planned rather than trusting a half-written output"},
	{PhaseBuilding, PhaseFailed, true,
		"discard any partially written build output"},

	{PhaseBuilt, PhasePublishing, true, ""},

	{PhasePublishing, PhasePublished, false,
		"a publish that crashed mid-upload may have left a partial or unverified artifact at the registry; re-verify by digest (never trust a previous 'published' claim) or re-upload before considering it Published"},
	{PhasePublishing, PhaseFailed, true,
		"an interrupted upload should be left for the registry's own garbage collection by digest, not deleted by this caller - a concurrent reader may already be resolving the same digest"},

	{PhasePublished, PhaseDeploying, true, ""},

	{PhaseDeploying, PhaseRunning, false,
		"a deploy that crashed mid-apply may have partially created the workload at the backend; reconcile against the backend's own observed state (Kubernetes/KubeVirt/containerd) before retrying - never assume nothing was created"},
	{PhaseDeploying, PhaseFailed, true,
		"reconcile against the backend's observed state and tear down whatever was partially created"},

	{PhaseRunning, PhaseStopping, true, ""},
	{PhaseStopping, PhaseStopped, true, ""},
	{PhaseStopping, PhaseFailed, true,
		"a workload that failed to stop cleanly needs the same reconciliation as a failed Deploying transition"},

	{PhaseRunning, PhaseDeleting, true, ""},
	{PhaseStopped, PhaseDeleting, true, ""},
	{PhaseFailed, PhaseDeleting, true, ""},
	{PhaseDeleting, PhaseDeleted, true,
		"deleting an already-deleted resource must be treated as success, not an error - this is what makes Deleting -> Deleted safe to repeat verbatim"},

	{PhaseFailed, PhaseDeclared, true,
		"retry the whole workload from scratch"},
}

func init() {
	// Every phase (other than Unknown itself) can degrade to Unknown -
	// this holds regardless of which phase it was in, since "the
	// external system couldn't be reached to confirm state" can happen
	// at any point, not just from specific phases. Generated here
	// rather than hand-listed 13 times above, so it can't silently miss
	// a phase added later to the const block without this list growing
	// too - see TestEveryPhaseCanDegradeToUnknown.
	for _, p := range []Phase{
		PhaseDeclared, PhasePlanned, PhaseBuilding, PhaseBuilt,
		PhasePublishing, PhasePublished, PhaseDeploying, PhaseRunning,
		PhaseStopping, PhaseStopped, PhaseDeleting, PhaseDeleted, PhaseFailed,
	} {
		transitions = append(transitions, TransitionRule{From: p, To: PhaseUnknown, Idempotent: true})
	}
	// Unknown must always be able to recover to a freshly re-queried
	// state such as Running or Deleted once the external system
	// answers again - not just back to Declared.
	for _, p := range []Phase{
		PhaseDeclared, PhaseBuilding, PhaseBuilt, PhasePublished,
		PhaseDeploying, PhaseRunning, PhaseStopped, PhaseDeleted, PhaseFailed,
	} {
		transitions = append(transitions, TransitionRule{From: PhaseUnknown, To: p, Idempotent: true})
	}
}

// LookupTransition returns the rule governing from -> to, if that
// transition is allowed.
func LookupTransition(from, to Phase) (TransitionRule, bool) {
	for _, t := range transitions {
		if t.From == from && t.To == to {
			return t, true
		}
	}
	return TransitionRule{}, false
}

// CanTransition reports whether from -> to is an allowed transition.
func CanTransition(from, to Phase) bool {
	_, ok := LookupTransition(from, to)
	return ok
}

// TransitionTo returns a new RuntimeState with Phase set to to, if the
// move from s.Phase is allowed. s itself is never mutated. An empty
// s.Phase is treated as PhaseDeclared's starting point implicitly only
// when to is PhaseDeclared itself (a freshly-declared workload has no
// prior phase to transition from); every other case requires an
// explicit, valid current phase.
func (s RuntimeState) TransitionTo(to Phase) (RuntimeState, error) {
	if !to.valid() {
		return s, fmt.Errorf("%q is not a recognized phase", to)
	}
	if s.Phase == "" && to == PhaseDeclared {
		return RuntimeState{Phase: PhaseDeclared}, nil
	}
	if !s.Phase.valid() {
		return s, fmt.Errorf("%q is not a recognized phase", s.Phase)
	}
	if !CanTransition(s.Phase, to) {
		return s, fmt.Errorf("no transition from %q to %q", s.Phase, to)
	}
	return RuntimeState{Phase: to}, nil
}

package projectinit

import "testing"

// unknownsFixture returns a fresh slice each call - FilterResolvedUnknowns
// filters in place (unknowns[:0] reuses the backing array), so reusing
// one slice across multiple calls in this test would corrupt it.
func unknownsFixture() []Unknown {
	return []Unknown{
		{Subject: "build.artifact", Reason: "no artifact detected"},
		{Subject: "dependencies", Reason: "dependency state unknown"},
		{Subject: "runtime", Reason: "runtime not chosen"},
	}
}

func TestFilterResolvedUnknownsDropsSettledSubjects(t *testing.T) {
	got := FilterResolvedUnknowns(unknownsFixture(), "main.py", "unknown")
	if len(got) != 2 || got[0].Subject != "dependencies" || got[1].Subject != "runtime" {
		t.Fatalf("got=%+v, want build.artifact dropped once artifact is set", got)
	}

	got = FilterResolvedUnknowns(unknownsFixture(), "", "none")
	if len(got) != 2 || got[0].Subject != "build.artifact" || got[1].Subject != "runtime" {
		t.Fatalf("got=%+v, want dependencies dropped once dependencyMode is resolved", got)
	}

	got = FilterResolvedUnknowns(unknownsFixture(), "main.py", "none")
	if len(got) != 1 || got[0].Subject != "runtime" {
		t.Fatalf("got=%+v, want only runtime left once both are resolved", got)
	}
}

package projectinit

import (
	"reflect"
	"testing"
)

func TestWithSystemProposalCanonicalizesWithoutMutatingInput(t *testing.T) {
	proposal := SystemProposal{
		Name: "business-suite",
		Components: []ComponentProposal{
			{Name: "legacy", Source: "legacy.qcow2", Runtime: RuntimeDecision{Recommended: RuntimeMicroVM, Selected: RuntimeMicroVM, Reasons: []string{"systemd", "kernel-module"}}},
			{Name: "gateway", Source: ".", Runtime: RuntimeDecision{Recommended: RuntimeContainer, Selected: RuntimeContainer, Reasons: []string{"stateless"}}},
		},
		Resources: []ResourceProposal{
			{Name: "vpn", Type: "network", Ownership: OwnershipShared, DeletionPolicy: DeletionRetain},
			{Name: "database", Type: "database", Ownership: OwnershipExternal, DeletionPolicy: DeletionRetain},
		},
		Connections: []ConnectionProposal{
			{From: "legacy", To: "vpn", Protocol: "external"},
			{From: "gateway", To: "database", Protocol: "postgres", Port: 5432},
		},
	}
	original := cloneSystemProposal(proposal)
	first, err := WithSystemProposal(Plan{}, proposal)
	if err != nil {
		t.Fatal(err)
	}
	second, err := WithSystemProposal(Plan{}, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(proposal, original) {
		t.Fatal("canonicalization mutated caller input")
	}
	if !reflect.DeepEqual(first.System, second.System) {
		t.Fatalf("proposal is nondeterministic: %#v != %#v", first.System, second.System)
	}
	if first.System.Components[0].Name != "gateway" || first.System.Resources[0].Name != "database" || first.System.Connections[0].From != "gateway" {
		t.Fatalf("proposal is not canonical: %#v", first.System)
	}
}

func TestSystemProposalPreservesUnresolvedRuntime(t *testing.T) {
	plan, err := WithSystemProposal(Plan{}, SystemProposal{Name: "suite", Components: []ComponentProposal{{
		Name: "legacy", Source: "legacy.raw", Runtime: RuntimeDecision{
			Recommended: RuntimeMicroVM,
			Reasons:     []string{"incomplete-container-separation"},
			Unknowns:    []Unknown{{Subject: "runtime.selected", Reason: "operator confirmation required"}},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.System.Components[0].Runtime.Selected != "" || len(plan.System.Components[0].Runtime.Unknowns) != 1 {
		t.Fatalf("unresolved selection was invented: %#v", plan.System.Components[0].Runtime)
	}
}

func TestSystemProposalRejectsUnsafeOrUnownedTopology(t *testing.T) {
	validRuntime := RuntimeDecision{Recommended: RuntimeContainer, Selected: RuntimeContainer, Reasons: []string{"stateless"}}
	tests := []struct {
		name     string
		proposal SystemProposal
	}{
		{"path traversal", SystemProposal{Name: "suite", Components: []ComponentProposal{{Name: "api", Source: "../api", Runtime: validRuntime}}}},
		{"duplicate endpoint", SystemProposal{Name: "suite", Components: []ComponentProposal{{Name: "db", Source: "db", Runtime: validRuntime}}, Resources: []ResourceProposal{{Name: "db", Type: "database", Ownership: OwnershipExternal, DeletionPolicy: DeletionRetain}}}},
		{"deletable external", SystemProposal{Name: "suite", Resources: []ResourceProposal{{Name: "db", Type: "database", Ownership: OwnershipExternal, DeletionPolicy: "delete"}}}},
		{"unknown endpoint", SystemProposal{Name: "suite", Components: []ComponentProposal{{Name: "api", Source: "api", Runtime: validRuntime}}, Connections: []ConnectionProposal{{From: "api", To: "missing", Protocol: "https"}}}},
		{"duplicate connection", SystemProposal{Name: "suite", Components: []ComponentProposal{{Name: "api", Source: "api", Runtime: validRuntime}, {Name: "worker", Source: "worker", Runtime: validRuntime}}, Connections: []ConnectionProposal{{From: "api", To: "worker", Protocol: "https"}, {From: "api", To: "worker", Protocol: "https"}}}},
		{"unexplained runtime", SystemProposal{Name: "suite", Components: []ComponentProposal{{Name: "api", Source: "api", Runtime: RuntimeDecision{Recommended: RuntimeContainer, Selected: RuntimeContainer}}}}},
		{"invented selection", SystemProposal{Name: "suite", Components: []ComponentProposal{{Name: "api", Source: "api", Runtime: RuntimeDecision{Recommended: RuntimeContainer, Reasons: []string{"stateless"}}}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := WithSystemProposal(Plan{}, test.proposal); err == nil {
				t.Fatal("expected proposal validation failure")
			}
		})
	}
}

func TestSystemProposalValidateRejectsNonAdjacentDuplicateConnection(t *testing.T) {
	runtime := RuntimeDecision{Recommended: RuntimeContainer, Selected: RuntimeContainer, Reasons: []string{"stateless"}}
	duplicate := ConnectionProposal{From: "api", To: "worker", Protocol: "https", Port: 8443}
	proposal := SystemProposal{
		Name: "suite",
		Components: []ComponentProposal{
			{Name: "api", Source: "api", Runtime: runtime},
			{Name: "worker", Source: "worker", Runtime: runtime},
		},
		Connections: []ConnectionProposal{
			duplicate,
			{From: "worker", To: "api", Protocol: "http", Port: 8080},
			duplicate,
		},
	}
	if err := proposal.Validate(); err == nil {
		t.Fatal("direct validation accepted non-adjacent duplicate connections")
	}
}

func TestWithSystemProposalCopiesExistingPlan(t *testing.T) {
	actions := []Action{{kind: ActionWriteFile, path: "state", content: []byte("original")}}
	unknowns := []Unknown{{Subject: "artifact", Reason: "not observed"}}
	plan, err := WithSystemProposal(Plan{Actions: actions, Unknowns: unknowns}, SystemProposal{Name: "empty-suite"})
	if err != nil {
		t.Fatal(err)
	}
	actions[0].path = "changed"
	actions[0].content[0] = 'X'
	unknowns[0].Reason = "changed"
	if plan.Actions[0].path != "state" || string(plan.Actions[0].content) != "original" || plan.Unknowns[0].Reason != "not observed" {
		t.Fatal("attached proposal aliases the source plan")
	}
	plan.Actions[0].content[0] = 'Y'
	plan.Unknowns[0].Reason = "returned changed"
	if string(actions[0].content) != "Xriginal" || unknowns[0].Reason != "changed" {
		t.Fatal("source plan aliases the returned plan")
	}
}

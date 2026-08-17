package projectinit

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInspectSystemDoesNotClassifyLanguageOwnedMarkers(t *testing.T) {
	dir := t.TempDir()
	writeMarker(t, dir, "worker", "Cargo.toml")
	writeMarker(t, dir, "api", "go.mod")
	first, err := InspectSystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := InspectSystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || len(first.Components) != 0 {
		t.Fatalf("host classified plugin-owned project markers: %#v", first)
	}
}

func TestInspectSystemDoesNotTraverseSymlinkedApplicationDirectory(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "project.marker"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "linked-app")); err != nil {
		t.Fatal(err)
	}
	proposal, err := InspectSystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Components) != 0 || len(proposal.Unknowns) != 1 || !strings.Contains(proposal.Unknowns[0].Reason, "symlinked") {
		t.Fatalf("proposal=%#v", proposal)
	}
}

func TestBuildPlanRemainsLanguageNeutralWithoutPluginProposal(t *testing.T) {
	dir := t.TempDir()
	writeMarker(t, dir, "api", "go.mod")
	plan, err := BuildPlan(dir, Ecosystem{}, nil, testObservations())
	if err != nil {
		t.Fatal(err)
	}
	if plan.System == nil || len(plan.System.Components) != 0 {
		t.Fatalf("proposal=%#v", plan.System)
	}
	if content := string(plan.Actions[0].content); !strings.Contains(content, "version: 1") || strings.Contains(content, "language:") {
		t.Fatalf("unexpected config: %q", content)
	}
}

func writeMarker(t *testing.T, root, component, marker string) {
	t.Helper()
	dir := filepath.Join(root, component)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, marker), []byte("marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

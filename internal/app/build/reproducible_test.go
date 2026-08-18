package build

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CYPT71/platform-factory/internal/project"
)

// writeMarkerLayout is a fake build's side effect: it must produce
// something at output, exactly like oci.Build does, so ReproducibleBuild
// has something real to rename/compare/restore.
func writeMarkerLayout(t *testing.T, output, marker string) {
	t.Helper()
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "marker"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readMarker(t *testing.T, output string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(output, "marker"))
	if errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data), true
}

func TestReproducibleBuildSameDigestLeavesFirstCandidateInPlace(t *testing.T) {
	root := t.TempDir()
	loaded := project.Loaded{Root: root, Config: project.Config{Output: "layout"}}
	output := loaded.Output()

	calls := 0
	build := func() (string, error) {
		calls++
		writeMarkerLayout(t, output, "same")
		return "sha256:same", nil
	}

	first, second, err := ReproducibleBuild(loaded, build)
	if err != nil {
		t.Fatal(err)
	}
	if first != "sha256:same" || second != "sha256:same" || calls != 2 {
		t.Fatalf("first=%q second=%q calls=%d", first, second, calls)
	}
	if marker, ok := readMarker(t, output); !ok || marker != "same" {
		t.Fatalf("marker=%q ok=%v, want the reproducible layout left at output", marker, ok)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatalf("expected output to still exist: %v", err)
	}
}

func TestReproducibleBuildDivergentDigestsRestoresPreExistingLayout(t *testing.T) {
	root := t.TempDir()
	loaded := project.Loaded{Root: root, Config: project.Config{Output: "layout"}}
	output := loaded.Output()
	writeMarkerLayout(t, output, "original")

	calls := 0
	digests := []string{"sha256:one", "sha256:two"}
	build := func() (string, error) {
		digest := digests[calls]
		calls++
		writeMarkerLayout(t, output, digest)
		return digest, nil
	}

	first, second, err := ReproducibleBuild(loaded, build)
	if err != nil {
		t.Fatal(err)
	}
	if first != "sha256:one" || second != "sha256:two" {
		t.Fatalf("first=%q second=%q", first, second)
	}
	marker, ok := readMarker(t, output)
	if !ok || marker != "original" {
		t.Fatalf("marker=%q ok=%v, want the pre-existing layout restored when digests diverge", marker, ok)
	}
}

func TestReproducibleBuildDivergentDigestsKeepFirstCandidateWithoutPriorLayout(t *testing.T) {
	root := t.TempDir()
	loaded := project.Loaded{Root: root, Config: project.Config{Output: "layout"}}
	output := loaded.Output()

	calls := 0
	digests := []string{"sha256:one", "sha256:two"}
	build := func() (string, error) {
		digest := digests[calls]
		calls++
		writeMarkerLayout(t, output, digest)
		return digest, nil
	}

	first, second, err := ReproducibleBuild(loaded, build)
	if err != nil {
		t.Fatal(err)
	}
	if first != "sha256:one" || second != "sha256:two" {
		t.Fatalf("first=%q second=%q", first, second)
	}
	marker, ok := readMarker(t, output)
	if !ok || marker != "sha256:one" {
		t.Fatalf("marker=%q ok=%v, want the first candidate retained for diagnosis when there was no prior layout", marker, ok)
	}
}

func TestReproducibleBuildFirstBuildFailureRestoresPreviousAndWrapsFailedBuild(t *testing.T) {
	root := t.TempDir()
	loaded := project.Loaded{Root: root, Config: project.Config{Output: "layout"}}
	output := loaded.Output()
	writeMarkerLayout(t, output, "original")

	boom := errors.New("boom")
	build := func() (string, error) { return "", boom }

	first, second, err := ReproducibleBuild(loaded, build)
	if first != "" || second != "" {
		t.Fatalf("first=%q second=%q, want both empty on first-build failure", first, second)
	}
	var failed *FailedBuild
	if !errors.As(err, &failed) || !errors.Is(err, boom) {
		t.Fatalf("err=%v, want a *FailedBuild wrapping the build callback's error", err)
	}
	marker, ok := readMarker(t, output)
	if !ok || marker != "original" {
		t.Fatalf("marker=%q ok=%v, want the previous layout restored after a first-build failure", marker, ok)
	}
}

func TestReproducibleBuildFirstBuildFailureWithoutPriorLayoutLeavesOutputAbsent(t *testing.T) {
	root := t.TempDir()
	loaded := project.Loaded{Root: root, Config: project.Config{Output: "layout"}}
	output := loaded.Output()

	build := func() (string, error) { return "", errors.New("boom") }

	if _, _, err := ReproducibleBuild(loaded, build); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat output err=%v, want ErrNotExist since there was nothing to restore", err)
	}
}

func TestReproducibleBuildSecondBuildFailureRestoresFirstCandidate(t *testing.T) {
	root := t.TempDir()
	loaded := project.Loaded{Root: root, Config: project.Config{Output: "layout"}}
	output := loaded.Output()

	calls := 0
	boom := errors.New("second build boom")
	build := func() (string, error) {
		calls++
		if calls == 1 {
			writeMarkerLayout(t, output, "first-candidate")
			return "sha256:first", nil
		}
		return "", boom
	}

	first, second, err := ReproducibleBuild(loaded, build)
	if first != "sha256:first" || second != "" {
		t.Fatalf("first=%q second=%q", first, second)
	}
	var failed *FailedBuild
	if !errors.As(err, &failed) || !errors.Is(err, boom) {
		t.Fatalf("err=%v, want a *FailedBuild wrapping the second build callback's error", err)
	}
	marker, ok := readMarker(t, output)
	if !ok || marker != "first-candidate" {
		t.Fatalf("marker=%q ok=%v, want the first candidate restored after a second-build failure", marker, ok)
	}
}

func TestReproducibleBuildWorkspacePreparationFailureIsNotAFailedBuild(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// output's parent directory is a regular file, so os.MkdirAll(parent)
	// must fail before build is ever invoked.
	loaded := project.Loaded{Root: root, Config: project.Config{Output: filepath.Join("blocker", "sub", "layout")}}

	called := false
	build := func() (string, error) { called = true; return "sha256:x", nil }

	_, _, err := ReproducibleBuild(loaded, build)
	if err == nil {
		t.Fatal("expected a workspace preparation error")
	}
	var failed *FailedBuild
	if errors.As(err, &failed) {
		t.Fatal("a workspace preparation failure must not be reported as a FailedBuild")
	}
	if called {
		t.Fatal("build should never be called when workspace preparation fails")
	}
}

func TestFailedBuildErrorAndUnwrap(t *testing.T) {
	inner := errors.New("inner failure")
	failed := &FailedBuild{Err: inner}
	if failed.Error() != "inner failure" {
		t.Fatalf("Error()=%q", failed.Error())
	}
	if !errors.Is(failed, inner) {
		t.Fatal("expected errors.Is to see through FailedBuild to its wrapped error")
	}
}

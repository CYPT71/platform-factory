package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitFixtureModule builds a real, small Go module (three packages: a
// leaf "widget", a "toaster" that imports it, and an unrelated
// "standalone") inside a real git repo with an initial commit, so
// AffectedPackages exercises the actual `git diff`/`go list -json`
// pipeline end-to-end rather than a mocked graph.
func gitFixtureModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/fixture\n\ngo 1.25\n")
	write("widget/widget.go", "package widget\n\nfunc New() string { return \"widget\" }\n")
	write("toaster/toaster.go", "package toaster\n\nimport \"example.com/fixture/widget\"\n\nfunc Make() string { return widget.New() }\n")
	write("standalone/standalone.go", "package standalone\n\nfunc Noop() {}\n")

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--initial-branch=main")
	run("config", "user.email", "fixture@example.com")
	run("config", "user.name", "Fixture")
	run("config", "commit.gpgsign", "false")
	run("add", "-A")
	run("commit", "-m", "initial")
	return dir
}

func TestAffectedPackagesReturnsNilWithNoChanges(t *testing.T) {
	dir := gitFixtureModule(t)
	packages, err := AffectedPackages(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 0 {
		t.Fatalf("packages=%v", packages)
	}
}

func TestAffectedPackagesIncludesReverseDependents(t *testing.T) {
	dir := gitFixtureModule(t)
	// Modify the leaf package: toaster depends on it and must be
	// included; standalone does not and must be excluded.
	if err := os.WriteFile(filepath.Join(dir, "widget", "widget.go"),
		[]byte("package widget\n\nfunc New() string { return \"widget v2\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	packages, err := AffectedPackages(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, p := range packages {
		found[p] = true
	}
	if !found["example.com/fixture/widget"] {
		t.Fatalf("expected the changed package itself, got %v", packages)
	}
	if !found["example.com/fixture/toaster"] {
		t.Fatalf("expected the reverse dependent toaster, got %v", packages)
	}
	if found["example.com/fixture/standalone"] {
		t.Fatalf("standalone does not depend on widget and must be excluded, got %v", packages)
	}
}

func TestAffectedPackagesIncludesAnUntrackedNewFile(t *testing.T) {
	dir := gitFixtureModule(t)
	if err := os.WriteFile(filepath.Join(dir, "standalone", "extra.go"),
		[]byte("package standalone\n\nfunc Extra() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	packages, err := AffectedPackages(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range packages {
		if p == "example.com/fixture/standalone" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the untracked new file's package, got %v", packages)
	}
}

func TestValidateFastProfileRunsGofmtAndVet(t *testing.T) {
	dir := gitFixtureModule(t)
	report, err := Validate(context.Background(), dir, "fast")
	if err != nil {
		t.Fatal(err)
	}
	if report.Profile != "fast" || !report.Valid {
		t.Fatalf("report=%+v", report)
	}
	if len(report.Steps) != 2 {
		t.Fatalf("steps=%v", report.Steps)
	}
}

func TestValidateRejectsAnUnknownProfile(t *testing.T) {
	dir := gitFixtureModule(t)
	if _, err := Validate(context.Background(), dir, "bogus"); err == nil {
		t.Fatal("expected an error for an unknown profile")
	}
}

func TestValidateAffectedProfileWithNoChangesReportsOK(t *testing.T) {
	dir := gitFixtureModule(t)
	report, err := Validate(context.Background(), dir, "affected")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Valid || len(report.Steps) != 1 || report.Steps[0].Status != "ok" {
		t.Fatalf("report=%+v", report)
	}
}

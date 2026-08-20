package core

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestGovulncheckStepIsSkippedWhenNotInstalled(t *testing.T) {
	if _, err := exec.LookPath("govulncheck"); err == nil {
		t.Skip("govulncheck is installed in this environment; the skip branch cannot be exercised")
	}
	step := govulncheckStep(context.Background(), t.TempDir())
	if step.Status != "skipped" || !strings.Contains(step.Output, "not installed") {
		t.Fatalf("step=%+v", step)
	}
}

// TestGofmtStepFlagsUnformattedFiles covers gofmtStep's own "gofmt -l
// found output" failure branch (distinct from a command execution
// error): gofmt -l exits 0 even when it lists unformatted files, so
// this is the one step in this file whose success/failure is decided by
// stdout content, not exit code - nothing in this package's other tests
// (which all use gofmt-clean fixtures) reaches it.
func TestGofmtStepFlagsUnformattedFiles(t *testing.T) {
	dir := gitFixtureModule(t)
	// gofmt -l only reports files that are not already gofmt-formatted;
	// inconsistent spacing around the func keyword is enough.
	if err := writeFile(t, dir, "widget/messy.go", "package widget\n\nfunc   Messy( )   {}\n"); err != nil {
		t.Fatal(err)
	}
	step := gofmtStep(context.Background(), dir)
	if step.Status != "failed" || !strings.Contains(step.Output, "messy.go") {
		t.Fatalf("step=%+v", step)
	}
}

func TestValidateFullProfileRunsEveryStep(t *testing.T) {
	dir := gitFixtureModule(t)
	report, err := Validate(context.Background(), dir, "full")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if report.Profile != "full" || len(report.Steps) != 5 {
		t.Fatalf("report=%+v", report)
	}
	// internal/archtest does not exist in this minimal fixture module, so
	// its own step fails - proving the full profile actually reaches and
	// runs every one of its five steps rather than stopping early.
	if report.Valid {
		t.Fatalf("expected report.Valid=false since the fixture has no internal/archtest package: %+v", report)
	}
}

// TestValidateAffectedProfileWithChangesRunsGoTestOnThem covers
// Validate's "affected" profile branch where AffectedPackages returns a
// non-empty list - TestValidateAffectedProfileWithNoChangesReportsOK
// only exercises the empty-list branch.
func TestValidateAffectedProfileWithChangesRunsGoTestOnThem(t *testing.T) {
	dir := gitFixtureModule(t)
	if err := writeFile(t, dir, "standalone/standalone.go", "package standalone\n\nfunc Noop() {}\n\nfunc Extra() {}\n"); err != nil {
		t.Fatal(err)
	}
	report, err := Validate(context.Background(), dir, "affected")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Steps) != 1 || !strings.Contains(report.Steps[0].Name, "go test") {
		t.Fatalf("report=%+v", report)
	}
}

func writeFile(t *testing.T, repoRoot, relative, content string) error {
	t.Helper()
	result, err := WriteFile(repoRoot, relative, content)
	if err != nil {
		return err
	}
	if !result.Written {
		t.Fatalf("WriteFile(%q) did not report written=true", relative)
	}
	return nil
}

func TestValidateSecurityProfileRunsAllFourChecks(t *testing.T) {
	dir := gitFixtureModule(t)
	report, err := Validate(context.Background(), dir, "security")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(report.Steps) != 4 {
		t.Fatalf("steps=%+v", report.Steps)
	}
}

func TestValidateToolHandlerParsesArgumentsAndReportsFailure(t *testing.T) {
	dir := gitFixtureModule(t)
	handler := ValidateToolHandler(dir)
	if _, err := handler(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Fatal("expected an error for malformed arguments")
	}
	if _, err := handler(context.Background(), nil); err != nil {
		t.Fatalf("empty arguments should default to the fast profile: %v", err)
	}
	if _, err := handler(context.Background(), json.RawMessage(`{"profile":"bogus"}`)); err == nil {
		t.Fatal("expected an error for an unknown profile")
	}
}

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

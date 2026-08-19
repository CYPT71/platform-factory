package core

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"

	"github.com/CYPT71/platform-factory/internal/mcp/toolerror"
)

// StepResult is one check pf_core_validate ran.
type StepResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "ok", "failed", or "skipped"
	Output string `json:"output,omitempty"`
}

// Report is the pf_core_validate payload.
type Report struct {
	Profile string       `json:"profile"`
	Valid   bool         `json:"valid"`
	Steps   []StepResult `json:"steps"`
}

const commandTimeout = 5 * time.Minute

func runCommand(ctx context.Context, repoRoot, name string, args ...string) StepResult {
	step := StepResult{Name: strings.Join(append([]string{name}, args...), " ")}
	cmdCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, name, args...)
	cmd.Dir = repoRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		step.Status = "failed"
		combined := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
		if len(combined) > 4000 {
			combined = combined[:4000] + "\n... (truncated)"
		}
		step.Output = combined
		return step
	}
	step.Status = "ok"
	if strings.TrimSpace(stdout.String()) != "" {
		// gofmt -l prints nothing on success but lists files on failure;
		// go vet/build/test are silent on success too. Any stdout on an
		// otherwise-successful run (e.g. gofmt -l finding unformatted
		// files without a non-zero exit) is still worth surfacing.
		step.Output = strings.TrimSpace(stdout.String())
	}
	return step
}

// gofmtStep runs gofmt -l . and fails if it lists any file - gofmt -l
// exits 0 even when it finds unformatted files, so success/failure here
// is decided by output, not exit code, unlike every other step.
func gofmtStep(ctx context.Context, repoRoot string) StepResult {
	step := StepResult{Name: "gofmt -l ."}
	cmdCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "gofmt", "-l", ".")
	cmd.Dir = repoRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		step.Status = "failed"
		step.Output = strings.TrimSpace(stderr.String())
		return step
	}
	if unformatted := strings.TrimSpace(stdout.String()); unformatted != "" {
		step.Status = "failed"
		step.Output = "unformatted files:\n" + unformatted
		return step
	}
	step.Status = "ok"
	return step
}

func govulncheckStep(ctx context.Context, repoRoot string) StepResult {
	step := StepResult{Name: "govulncheck ./..."}
	if _, err := exec.LookPath("govulncheck"); err != nil {
		step.Status = "skipped"
		step.Output = "govulncheck is not installed"
		return step
	}
	return runCommand(ctx, repoRoot, "govulncheck", "./...")
}

// Validate runs one of four check profiles against repoRoot:
//   - fast: gofmt + go vet - a few seconds, safe to run on every save.
//   - full: fast, plus go build, go test ./..., and go test
//     ./internal/archtest/... (the import-boundary rules pf_core_patch's
//     own self-check also runs).
//   - security: the local, offline mirrors of ci-security.yml's static
//     checks (subprocess-execution allowlist, TLS-verification-bypass,
//     unfinished-work markers) plus govulncheck when available.
//   - affected: go test scoped to only the packages that changed files
//     (working tree vs HEAD, plus untracked files) affect, computed via
//     the real reverse-dependency graph (see affected.go).
func Validate(ctx context.Context, repoRoot, profile string) (Report, error) {
	report := Report{Profile: profile, Valid: true}
	switch profile {
	case "", "fast":
		report.Profile = "fast"
		report.Steps = []StepResult{
			gofmtStep(ctx, repoRoot),
			runCommand(ctx, repoRoot, "go", "vet", "./..."),
		}
	case "full":
		report.Steps = []StepResult{
			gofmtStep(ctx, repoRoot),
			runCommand(ctx, repoRoot, "go", "vet", "./..."),
			runCommand(ctx, repoRoot, "go", "build", "./..."),
			runCommand(ctx, repoRoot, "go", "test", "./..."),
			runCommand(ctx, repoRoot, "go", "test", "./internal/archtest/..."),
		}
	case "security":
		report.Steps = []StepResult{
			osExecAllowlistCheck(repoRoot),
			insecureSkipVerifyCheck(repoRoot),
			unfinishedWorkCheck(repoRoot),
			govulncheckStep(ctx, repoRoot),
		}
	case "affected":
		packages, err := AffectedPackages(ctx, repoRoot)
		if err != nil {
			return Report{}, err
		}
		if len(packages) == 0 {
			report.Steps = []StepResult{{Name: "go test (affected)", Status: "ok", Output: "no changed .go files; nothing to test"}}
		} else {
			args := append([]string{"test"}, packages...)
			report.Steps = []StepResult{runCommand(ctx, repoRoot, "go", args...)}
		}
	default:
		return Report{}, toolerror.New(toolerror.ErrInvalidArgument, "unknown profile %q; want fast, full, security, or affected", profile)
	}

	for _, step := range report.Steps {
		if step.Status == "failed" {
			report.Valid = false
		}
	}
	return report, nil
}

type validateArguments struct {
	Profile string `json:"profile"`
}

// ValidateToolHandler returns the pf_core_validate handler.
func ValidateToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var args validateArguments
		if len(arguments) > 0 && string(arguments) != "{}" {
			if err := json.Unmarshal(arguments, &args); err != nil {
				return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
			}
		}
		report, err := Validate(ctx, repoRoot, args.Profile)
		if err != nil {
			return "", err
		}
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

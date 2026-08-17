// Package git provides the MCP server's only path to git/gh: typed,
// argument-array operations (status, branch, commit, push, PR) confined
// to one repository directory, mirroring internal/marketplace/sync.go's
// existing gitEnv/runGit hardening. There is deliberately no "run
// arbitrary git command" primitive - every exported function does one
// named thing with typed parameters, so the MCP tool surface built on
// top of this package can never be handed a free-form git argument
// list.
//
// NOTE for internal/mcp/../.github/workflows/ci-security.yml's os/exec
// allowlist: this file's exec.Command/exec.CommandContext calls are the
// MCP server's git/gh integration point, added to that workflow's
// allowlist alongside the existing internal/marketplace/sync.go entry.
package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Repo is one git working tree the MCP server is allowed to operate on.
// Every method is confined to Dir; there is no way to address a path
// outside it.
type Repo struct {
	Dir string
}

// New returns a Repo rooted at dir. It does not verify dir is a git
// repository - callers that need that guarantee should call Status
// first, which fails clearly if it is not.
func New(dir string) *Repo { return &Repo{Dir: dir} }

// protectedBranches are refused as the target of any mutating
// operation this package exposes (branch creation happens FROM them,
// never TO them; commit/push never target them directly).
var protectedBranches = map[string]bool{"main": true, "master": true}

// IsProtectedBranch reports whether name is a branch this package
// refuses to let the MCP server commit to, push to, or delete.
func IsProtectedBranch(name string) bool { return protectedBranches[strings.TrimSpace(name)] }

// gitEnv disables anything that could make an automated call hang
// waiting on a human or leak credentials through a pager/prompt.
func gitEnv() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "GIT_PAGER=cat", "GIT_CONFIG_NOSYSTEM=1")
}

func (r *Repo) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	cmd.Env = gitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// runGH invokes the GitHub CLI the same way: argument array, hardened
// environment, no shell. It relies entirely on gh's own already-
// authenticated credential storage - this package never reads, stores,
// or forwards a GitHub token itself.
func (r *Repo) runGH(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1", "GIT_PAGER=cat")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// Status is a repository snapshot: current branch, dirty/clean, and the
// short status lines a caller can present without shelling out itself.
type Status struct {
	Branch    string
	Dirty     bool
	Porcelain string
}

// Status reports the working tree's current branch and dirty state.
func (r *Repo) Status(ctx context.Context) (Status, error) {
	branch, err := r.run(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return Status{}, fmt.Errorf("determine current branch: %w", err)
	}
	porcelain, err := r.run(ctx, "status", "--porcelain")
	if err != nil {
		return Status{}, fmt.Errorf("read working tree status: %w", err)
	}
	trimmedBranch := strings.TrimSpace(branch)
	return Status{
		Branch:    trimmedBranch,
		Dirty:     strings.TrimSpace(porcelain) != "",
		Porcelain: porcelain,
	}, nil
}

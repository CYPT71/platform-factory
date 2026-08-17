package git

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// branchNamePattern accepts a conservative subset of valid git ref
// names: no spaces, no "..", no leading "-", ASCII letters/digits plus
// "/", "_", "-", "." as separators. This is stricter than what git
// itself allows, deliberately - the MCP server only ever needs to
// create branches it named itself.
var branchNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/_.-]*$`)

// ValidBranchName reports whether name is safe to pass to git as a ref
// name and is not a protected branch.
func ValidBranchName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("branch name must not be empty")
	}
	if IsProtectedBranch(name) {
		return fmt.Errorf("branch name %q is protected", name)
	}
	if strings.Contains(name, "..") || strings.HasPrefix(name, "-") || strings.HasSuffix(name, "/") {
		return fmt.Errorf("branch name %q is not a safe git ref", name)
	}
	if !branchNamePattern.MatchString(name) {
		return fmt.Errorf("branch name %q contains characters that are not allowed", name)
	}
	return nil
}

// PrepareBranch checks out a new branch named name from the current
// HEAD and returns the branch it started from. It refuses: a protected
// or otherwise unsafe name, a name that already exists, and starting
// from a dirty working tree (a core-patch workflow always starts a
// branch from clean state so the diff it later commits is exactly, and
// only, the change it made).
func (r *Repo) PrepareBranch(ctx context.Context, name string) (startedFrom string, err error) {
	if err := ValidBranchName(name); err != nil {
		return "", err
	}
	status, err := r.Status(ctx)
	if err != nil {
		return "", err
	}
	if status.Dirty {
		return "", fmt.Errorf("refusing to branch from a dirty working tree; commit or stash first")
	}
	if _, err := r.run(ctx, "rev-parse", "--verify", "--quiet", "refs/heads/"+name); err == nil {
		return "", fmt.Errorf("branch %q already exists", name)
	}
	if _, err := r.run(ctx, "checkout", "-b", name); err != nil {
		return "", fmt.Errorf("create branch %q: %w", name, err)
	}
	return status.Branch, nil
}

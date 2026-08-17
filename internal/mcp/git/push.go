package git

import (
	"context"
	"fmt"
)

// Push pushes the current branch to remote (typically "origin"),
// setting the upstream. It refuses to push a protected branch and
// refuses to push while HEAD is detached.
func (r *Repo) Push(ctx context.Context, remote string) error {
	if remote == "" {
		remote = "origin"
	}
	status, err := r.Status(ctx)
	if err != nil {
		return err
	}
	if status.Branch == "" || status.Branch == "HEAD" {
		return fmt.Errorf("refusing to push a detached HEAD")
	}
	if IsProtectedBranch(status.Branch) {
		return fmt.Errorf("refusing to push directly to protected branch %q", status.Branch)
	}
	// --no-verify is never used here: the request's explicit prohibitions
	// list forbids bypassing verification, and pre-push hooks are exactly
	// the kind of validation this server must not skip.
	if _, err := r.run(ctx, "push", "-u", remote, status.Branch); err != nil {
		return fmt.Errorf("push %s to %s: %w", status.Branch, remote, err)
	}
	return nil
}

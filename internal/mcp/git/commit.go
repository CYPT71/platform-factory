package git

import (
	"context"
	"fmt"
	"strings"
)

// Commit stages exactly the given repo-relative paths and commits them
// with message. It refuses to commit directly to a protected branch -
// every real commit this package makes happens on a branch PrepareBranch
// created first.
func (r *Repo) Commit(ctx context.Context, paths []string, message string) error {
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("commit message must not be empty")
	}
	if len(paths) == 0 {
		return fmt.Errorf("commit requires at least one path")
	}
	status, err := r.Status(ctx)
	if err != nil {
		return err
	}
	if IsProtectedBranch(status.Branch) {
		return fmt.Errorf("refusing to commit directly to protected branch %q", status.Branch)
	}
	addArgs := append([]string{"add", "--"}, paths...)
	if _, err := r.run(ctx, addArgs...); err != nil {
		return fmt.Errorf("stage changes: %w", err)
	}
	if _, err := r.run(ctx, "commit", "-m", message); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

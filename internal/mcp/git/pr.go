package git

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/CYPT71/platform-factory/internal/mcp/toolerror"
)

// TargetRepoEnv names the environment variable that pins every PR this
// server opens to one explicit GitHub repository (owner/repo, e.g.
// "CYPT71/platform-factory") regardless of what "origin" happens to
// resolve to for the working tree PrepareBranch/Commit/Push operate on.
// Without it, gh infers the repo from the working tree the same way it
// always has - this only matters when a caller needs certainty that a
// PR lands on one specific repo.
const TargetRepoEnv = "PLATFORM_FACTORY_MCP_TARGET_REPO"

// TargetRepoFromEnv reads TargetRepoEnv, trimmed. Empty means "let gh
// infer the repo from the working tree," gh's own default behavior.
func TargetRepoFromEnv() string {
	return strings.TrimSpace(os.Getenv(TargetRepoEnv))
}

// PullRequest describes a draft PR to open. Base defaults to "main" when
// empty - this package has no notion of opening a PR against anything
// other than a protected branch, since that is the only kind of change
// this server is meant to propose for review. Repo, when non-empty,
// pins the PR to that exact "owner/repo" via gh's --repo flag instead
// of letting gh infer it from the working tree's own remotes.
type PullRequest struct {
	Base  string
	Head  string
	Title string
	Body  string
	Repo  string
}

// CreatePR opens a draft pull request via the already-authenticated gh
// CLI and returns its URL. There is deliberately no corresponding
// "merge" or "close" function anywhere in this package - opening the PR
// is the last mutating action this server ever takes on a core change;
// everything past that point is human review.
func (r *Repo) CreatePR(ctx context.Context, pr PullRequest) (string, error) {
	base := strings.TrimSpace(pr.Base)
	if base == "" {
		base = "main"
	}
	if !IsProtectedBranch(base) {
		return "", toolerror.New(toolerror.ErrInvalidArgument, "base %q must be the repository's protected branch", base)
	}
	head := strings.TrimSpace(pr.Head)
	if head == "" {
		return "", toolerror.New(toolerror.ErrInvalidArgument, "head branch must not be empty")
	}
	if IsProtectedBranch(head) {
		return "", toolerror.New(toolerror.ErrInvalidArgument, "refusing to open a PR whose head is the protected branch %q", head)
	}
	if strings.TrimSpace(pr.Title) == "" {
		return "", toolerror.New(toolerror.ErrInvalidArgument, "PR title must not be empty")
	}
	args := []string{"pr", "create",
		"--draft",
		"--base", base,
		"--head", head,
		"--title", pr.Title,
		"--body", pr.Body,
	}
	if repo := strings.TrimSpace(pr.Repo); repo != "" {
		args = append(args, "--repo", repo)
	}
	output, err := r.runGH(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("create draft pull request: %w", err)
	}
	return strings.TrimSpace(lastLine(output)), nil
}

// lastLine returns the last non-empty line of output - `gh pr create`
// prints progress notes followed by the PR URL as its final line.
func lastLine(output string) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	for _, line := range slices.Backward(lines) {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// --- exec.go ---------------------------------------------------------

func TestIsProtectedBranchTrimsWhitespaceAndOnlyMatchesKnownNames(t *testing.T) {
	cases := map[string]bool{
		"main":       true,
		"master":     true,
		" main\n":    true,
		"mcp/feat/x": false,
		"":           false,
	}
	for name, want := range cases {
		if got := IsProtectedBranch(name); got != want {
			t.Errorf("IsProtectedBranch(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestGitEnvHardensAgainstPromptsAndPagers(t *testing.T) {
	env := gitEnv()
	want := []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"GIT_PAGER=cat",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	for _, w := range want {
		found := false
		for _, kv := range env {
			if kv == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("gitEnv() missing %q, got %v", w, env)
		}
	}
}

func TestStatusFailsClearlyForANonGitDirectory(t *testing.T) {
	dir := t.TempDir() // exists on disk, but `git init` was never run
	r := New(dir)
	if _, err := r.Status(context.Background()); err == nil {
		t.Fatal("expected an error for a directory that is not a git repository")
	}
}

func TestStatusFailsClearlyWhenThePorcelainReadFails(t *testing.T) {
	// `rev-parse --abbrev-ref HEAD` and `status --porcelain` are two
	// separate git invocations inside Status; a corrupt index lets the
	// first succeed while the second fails, exercising that second
	// error-wrapping branch specifically.
	dir := newTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(dir)
	_, err := r.Status(context.Background())
	if err == nil {
		t.Fatal("expected an error reading working tree status with a corrupt index")
	}
	if !strings.Contains(err.Error(), "read working tree status") {
		t.Fatalf("err=%v", err)
	}
}

// --- branch.go ---------------------------------------------------------

func TestPrepareBranchFailsClearlyWhenGitRejectsAnOtherwiseValidatedName(t *testing.T) {
	// ValidBranchName's checks are deliberately looser than git's own
	// ref-name rules (see branch.go's comment on branchNamePattern): a
	// trailing "." passes ValidBranchName but `git check-ref-format`
	// refuses it. PrepareBranch must surface that as a normal error
	// rather than leaving the working tree in a half-switched state.
	dir := newTestRepo(t)
	r := New(dir)
	name := "mcp/feat/example."
	if err := ValidBranchName(name); err != nil {
		t.Fatalf("precondition failed: ValidBranchName(%q) = %v", name, err)
	}
	if _, err := r.PrepareBranch(context.Background(), name); err == nil {
		t.Fatal("expected git itself to reject the trailing-dot branch name")
	}
	status, err := r.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Branch != "main" {
		t.Fatalf("expected to remain on main after the failed checkout, got %q", status.Branch)
	}
}

func TestPrepareBranchStartedFromReflectsWhicheverBranchIsCurrent(t *testing.T) {
	dir := newTestRepo(t)
	r := New(dir)
	if _, err := r.PrepareBranch(context.Background(), "mcp/feat/base"); err != nil {
		t.Fatal(err)
	}
	// Commit something on the base branch so the tree is clean again and
	// a second branch can be prepared from it.
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(context.Background(), []string{"base.txt"}, "base work"); err != nil {
		t.Fatal(err)
	}

	startedFrom, err := r.PrepareBranch(context.Background(), "mcp/feat/child")
	if err != nil {
		t.Fatal(err)
	}
	if startedFrom != "mcp/feat/base" {
		t.Fatalf("startedFrom=%q, want %q", startedFrom, "mcp/feat/base")
	}
}

// --- commit.go ---------------------------------------------------------

func TestCommitRefusesAnEmptyPathList(t *testing.T) {
	dir := newTestRepo(t)
	r := New(dir)
	if _, err := r.PrepareBranch(context.Background(), "mcp/feat/example"); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(context.Background(), nil, "message"); err == nil {
		t.Fatal("expected an error committing with no paths")
	}
}

func TestCommitFailsClearlyWhenAPathDoesNotExist(t *testing.T) {
	dir := newTestRepo(t)
	r := New(dir)
	if _, err := r.PrepareBranch(context.Background(), "mcp/feat/example"); err != nil {
		t.Fatal(err)
	}
	err := r.Commit(context.Background(), []string{"does-not-exist.txt"}, "message")
	if err == nil {
		t.Fatal("expected an error staging a nonexistent path")
	}
	if !strings.Contains(err.Error(), "stage changes") {
		t.Fatalf("err=%v, want it wrapped as a staging failure", err)
	}
}

func TestCommitFailsClearlyWhenThereIsNothingToCommit(t *testing.T) {
	dir := newTestRepo(t)
	r := New(dir)
	if _, err := r.PrepareBranch(context.Background(), "mcp/feat/example"); err != nil {
		t.Fatal(err)
	}
	// README.md is already committed by the fixture and untouched, so
	// staging it is a no-op and the commit itself has nothing to record.
	err := r.Commit(context.Background(), []string{"README.md"}, "no-op")
	if err == nil {
		t.Fatal("expected an error committing with no staged changes")
	}
	if !strings.Contains(err.Error(), "commit:") {
		t.Fatalf("err=%v, want it wrapped as a commit failure", err)
	}
}

// --- push.go ---------------------------------------------------------

func TestPushRefusesADetachedHead(t *testing.T) {
	dir := newTestRepo(t)
	sha := strings.TrimSpace(runFixtureGit(t, dir, "rev-parse", "HEAD"))
	runFixtureGit(t, dir, "checkout", "--detach", sha)

	r := New(dir)
	err := r.Push(context.Background(), "origin")
	if err == nil {
		t.Fatal("expected an error pushing a detached HEAD")
	}
	if !strings.Contains(err.Error(), "detached HEAD") {
		t.Fatalf("err=%v", err)
	}
}

func TestPushFailsClearlyForAnUnknownRemote(t *testing.T) {
	dir := newTestRepo(t)
	r := New(dir)
	if _, err := r.PrepareBranch(context.Background(), "mcp/feat/example"); err != nil {
		t.Fatal(err)
	}
	err := r.Push(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("expected an error pushing to a remote that was never configured")
	}
	if !strings.Contains(err.Error(), "push mcp/feat/example to does-not-exist") {
		t.Fatalf("err=%v", err)
	}
}

func TestPushDefaultsToOriginWhenRemoteIsEmpty(t *testing.T) {
	dir := newTestRepo(t)
	remote := t.TempDir()
	runFixtureGit(t, remote, "init", "--bare", "--initial-branch=main")
	runFixtureGit(t, dir, "remote", "add", "origin", remote)

	r := New(dir)
	if _, err := r.PrepareBranch(context.Background(), "mcp/feat/example"); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(context.Background(), ""); err != nil {
		t.Fatal(err)
	}

	refs := runFixtureGit(t, remote, "branch")
	if !strings.Contains(refs, "mcp/feat/example") {
		t.Fatalf("expected the push to default to origin, got remote branches: %q", refs)
	}
}

// --- pr.go ---------------------------------------------------------

func TestTargetRepoFromEnvReadsAndTrimsTheEnvironmentVariable(t *testing.T) {
	t.Setenv(TargetRepoEnv, "")
	if got := TargetRepoFromEnv(); got != "" {
		t.Fatalf("got %q, want empty when unset", got)
	}
	t.Setenv(TargetRepoEnv, "  owner/repo  \n")
	if got := TargetRepoFromEnv(); got != "owner/repo" {
		t.Fatalf("got %q, want %q", got, "owner/repo")
	}
}

func TestCreatePRSucceedsAndReturnsTheLastLineOfGHsOutput(t *testing.T) {
	dir := newTestRepo(t)
	newFakeGH(t, "#!/bin/sh\necho \"Creating pull request\"\necho \"https://github.com/x/y/pull/7\"\n")

	r := New(dir)
	url, err := r.CreatePR(context.Background(), PullRequest{Head: "mcp/feat/example", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/x/y/pull/7" {
		t.Fatalf("url=%q", url)
	}
}

func TestCreatePRPassesTheRepoFlagWhenPinned(t *testing.T) {
	dir := newTestRepo(t)
	argsFile := filepath.Join(t.TempDir(), "gh-args.txt")
	newFakeGH(t, "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > \""+argsFile+"\"\necho \"https://github.com/owner/repo/pull/1\"\n")

	r := New(dir)
	_, err := r.CreatePR(context.Background(), PullRequest{
		Head: "mcp/feat/example", Title: "t", Repo: "owner/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	recorded, readErr := os.ReadFile(argsFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(recorded), "--repo\nowner/repo") {
		t.Fatalf("expected --repo owner/repo among gh's arguments, got: %q", recorded)
	}
}

func TestCreatePRWrapsAGHFailure(t *testing.T) {
	dir := newTestRepo(t)
	newFakeGH(t, "#!/bin/sh\necho \"not authenticated\" >&2\nexit 1\n")

	r := New(dir)
	_, err := r.CreatePR(context.Background(), PullRequest{Head: "mcp/feat/example", Title: "t"})
	if err == nil {
		t.Fatal("expected gh's failure to propagate")
	}
	if !strings.Contains(err.Error(), "create draft pull request") || !strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("err=%v", err)
	}
}

func TestCreatePRFailsClearlyWhenGHIsNotOnPATH(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found on PATH")
	}
	t.Setenv("PATH", filepath.Dir(gitPath))
	if _, err := exec.LookPath("gh"); err == nil {
		t.Skip("gh is still reachable after narrowing PATH to git's directory; environment is unusual, skipping")
	}

	dir := newTestRepo(t)
	r := New(dir)
	_, err = r.CreatePR(context.Background(), PullRequest{Head: "mcp/feat/example", Title: "t"})
	if err == nil {
		t.Fatal("expected an error when gh is not on PATH")
	}
	if !strings.Contains(err.Error(), "create draft pull request") {
		t.Fatalf("err=%v", err)
	}
}

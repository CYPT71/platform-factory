package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusReportsBranchAndDirtyState(t *testing.T) {
	dir := newTestRepo(t)
	r := New(dir)

	status, err := r.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Branch != "main" {
		t.Fatalf("branch=%q", status.Branch)
	}
	if status.Dirty {
		t.Fatal("expected a clean working tree right after the fixture commit")
	}

	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err = r.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Dirty {
		t.Fatal("expected a dirty working tree after adding an untracked file")
	}
}

func TestValidBranchNameRejectsProtectedAndUnsafeNames(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"mcp/feat/plugin-detector-api", false},
		{"main", true},
		{"master", true},
		{"", true},
		{"-x", true},
		{"a..b", true},
		{"trailing/", true},
		{"has space", true},
	}
	for _, c := range cases {
		err := ValidBranchName(c.name)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidBranchName(%q) error=%v, wantErr=%v", c.name, err, c.wantErr)
		}
	}
}

func TestPrepareBranchCreatesAndChecksOutANewBranch(t *testing.T) {
	dir := newTestRepo(t)
	r := New(dir)

	startedFrom, err := r.PrepareBranch(context.Background(), "mcp/feat/example")
	if err != nil {
		t.Fatal(err)
	}
	if startedFrom != "main" {
		t.Fatalf("startedFrom=%q", startedFrom)
	}
	status, err := r.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Branch != "mcp/feat/example" {
		t.Fatalf("branch=%q", status.Branch)
	}
}

// TestPrepareBranchPropagatesAStatusError covers PrepareBranch's own
// r.Status error branch: on a directory that was never `git init`-ed,
// Status itself fails, which PrepareBranch must propagate rather than
// treating as a "not dirty" or otherwise recoverable state.
func TestPrepareBranchPropagatesAStatusError(t *testing.T) {
	dir := t.TempDir() // deliberately never `git init`-ed
	r := New(dir)
	if _, err := r.PrepareBranch(context.Background(), "mcp/feat/example"); err == nil {
		t.Fatal("expected an error for a non-repository directory")
	}
}

func TestPrepareBranchRefusesAProtectedName(t *testing.T) {
	dir := newTestRepo(t)
	r := New(dir)
	if _, err := r.PrepareBranch(context.Background(), "main"); err == nil {
		t.Fatal("expected an error for a protected branch name")
	}
}

func TestPrepareBranchRefusesADirtyWorkingTree(t *testing.T) {
	dir := newTestRepo(t)
	r := New(dir)
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PrepareBranch(context.Background(), "mcp/feat/example"); err == nil {
		t.Fatal("expected an error for a dirty working tree")
	}
}

func TestPrepareBranchRefusesADuplicateName(t *testing.T) {
	dir := newTestRepo(t)
	r := New(dir)
	if _, err := r.PrepareBranch(context.Background(), "mcp/feat/example"); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, dir, "checkout", "main")
	if _, err := r.PrepareBranch(context.Background(), "mcp/feat/example"); err == nil {
		t.Fatal("expected an error for an already-existing branch")
	}
}

func TestCommitStagesOnlyGivenPathsAndRefusesOnProtectedBranch(t *testing.T) {
	dir := newTestRepo(t)
	r := New(dir)

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(context.Background(), []string{"a.txt"}, "add a"); err == nil {
		t.Fatal("expected an error committing directly to main")
	}
	if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}

	if _, err := r.PrepareBranch(context.Background(), "mcp/feat/example"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(context.Background(), []string{"a.txt"}, "add a"); err != nil {
		t.Fatal(err)
	}
	status, err := r.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Dirty {
		t.Fatal("expected b.txt to remain uncommitted since only a.txt was staged")
	}
	if !strings.Contains(status.Porcelain, "b.txt") {
		t.Fatalf("expected b.txt to still show as untracked, got: %q", status.Porcelain)
	}
}

func TestCommitRefusesAnEmptyMessage(t *testing.T) {
	dir := newTestRepo(t)
	r := New(dir)
	if _, err := r.PrepareBranch(context.Background(), "mcp/feat/example"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(context.Background(), []string{"a.txt"}, "   "); err == nil {
		t.Fatal("expected an error for a blank commit message")
	}
}

// TestCommitPropagatesAStatusError covers Commit's own r.Status error
// branch - every other Commit test in this file drives a real fixture
// repo.
func TestCommitPropagatesAStatusError(t *testing.T) {
	dir := t.TempDir() // deliberately never `git init`-ed
	r := New(dir)
	if err := r.Commit(context.Background(), []string{"a.txt"}, "message"); err == nil {
		t.Fatal("expected an error for a non-repository directory")
	}
}

func TestPushRefusesAProtectedBranch(t *testing.T) {
	dir := newTestRepo(t)
	r := New(dir)
	if err := r.Push(context.Background(), "origin"); err == nil {
		t.Fatal("expected an error pushing main directly")
	}
}

// TestPushPropagatesAStatusError covers Push's own r.Status error
// branch.
func TestPushPropagatesAStatusError(t *testing.T) {
	dir := t.TempDir() // deliberately never `git init`-ed
	r := New(dir)
	if err := r.Push(context.Background(), "origin"); err == nil {
		t.Fatal("expected an error for a non-repository directory")
	}
}

func TestPushSucceedsAgainstALocalBareRemote(t *testing.T) {
	dir := newTestRepo(t)
	remote := t.TempDir()
	runFixtureGit(t, remote, "init", "--bare", "--initial-branch=main")

	r := New(dir)
	runFixtureGit(t, dir, "remote", "add", "origin", remote)
	if _, err := r.PrepareBranch(context.Background(), "mcp/feat/example"); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(context.Background(), "origin"); err != nil {
		t.Fatal(err)
	}

	refs := runFixtureGit(t, remote, "branch")
	if !strings.Contains(refs, "mcp/feat/example") {
		t.Fatalf("expected the pushed branch on the remote, got: %q", refs)
	}
}

func TestCreatePRRefusesANonProtectedBase(t *testing.T) {
	dir := newTestRepo(t)
	r := New(dir)
	_, err := r.CreatePR(context.Background(), PullRequest{
		Base: "mcp/feat/example", Head: "mcp/feat/other", Title: "x",
	})
	if err == nil {
		t.Fatal("expected an error for a non-protected base")
	}
}

func TestCreatePRRefusesAProtectedHead(t *testing.T) {
	dir := newTestRepo(t)
	r := New(dir)
	_, err := r.CreatePR(context.Background(), PullRequest{Head: "main", Title: "x"})
	if err == nil {
		t.Fatal("expected an error when head is the protected branch")
	}
}

func TestCreatePRRefusesAnEmptyTitle(t *testing.T) {
	dir := newTestRepo(t)
	r := New(dir)
	_, err := r.CreatePR(context.Background(), PullRequest{Head: "mcp/feat/example"})
	if err == nil {
		t.Fatal("expected an error for an empty title")
	}
}

// TestCreatePRRefusesAnEmptyHead covers CreatePR's own empty-Head
// validation branch, the sibling of TestCreatePRRefusesAProtectedHead's
// (a protected, non-empty Head) and TestCreatePRRefusesAnEmptyTitle's
// (a valid Head, empty Title) cases.
func TestCreatePRRefusesAnEmptyHead(t *testing.T) {
	dir := newTestRepo(t)
	r := New(dir)
	_, err := r.CreatePR(context.Background(), PullRequest{Title: "x"})
	if err == nil {
		t.Fatal("expected an error for an empty head branch")
	}
}

func TestLastLineReturnsTheFinalNonEmptyLine(t *testing.T) {
	cases := map[string]string{
		"https://github.com/x/y/pull/1\n":                "https://github.com/x/y/pull/1",
		"Creating PR\nhttps://github.com/x/y/pull/1\n\n": "https://github.com/x/y/pull/1",
		"": "",
	}
	for input, want := range cases {
		if got := lastLine(input); got != want {
			t.Errorf("lastLine(%q) = %q, want %q", input, got, want)
		}
	}
}

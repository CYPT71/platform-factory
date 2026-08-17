package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// newTestRepo creates a real local git repository with one initial
// commit on main - mirrors internal/marketplace/gitfixture_test.go's
// newTestRepo, since that is this codebase's established convention for
// testing git-shelling code against the real binary instead of mocking
// it.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runFixtureGit(t, dir, "init", "--initial-branch=main")
	runFixtureGit(t, dir, "config", "user.email", "fixture@example.com")
	runFixtureGit(t, dir, "config", "user.name", "Fixture")
	runFixtureGit(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, dir, "add", "-A")
	runFixtureGit(t, dir, "commit", "-m", "initial commit")
	return dir
}

func runFixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

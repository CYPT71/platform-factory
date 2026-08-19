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

// newFakeGH writes a fake `gh` executable (a shell script) to a fresh
// directory and prepends that directory to PATH for the duration of the
// test (t.Setenv restores it afterwards), so exec.LookPath("gh") - and
// therefore r.runGH - resolves to it instead of a real gh binary.
//
// This exists because this sandbox's real `gh` is authenticated against
// a live GitHub account (see `gh auth status`): a test that shelled out
// to the real binary could actually open a pull request or otherwise
// touch a real repository over the network. Every gh success/failure
// path this package needs to cover is instead exercised against this
// deterministic, local-only stand-in - never the real CLI.
func newFakeGH(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

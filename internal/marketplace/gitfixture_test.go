package marketplace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// newTestRepo creates a real local Git repository (no mocking - every
// sync/install test in this package exercises the actual `git` binary
// against it) and returns its filesystem path, usable directly as a
// clone/ls-remote source.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runFixtureGit(t, dir, "init", "--initial-branch=main")
	runFixtureGit(t, dir, "config", "user.email", "fixture@example.com")
	runFixtureGit(t, dir, "config", "user.name", "Fixture")
	runFixtureGit(t, dir, "config", "commit.gpgsign", "false")
	runFixtureGit(t, dir, "config", "tag.gpgsign", "false")
	return dir
}

// tagRelease commits manifest as plugin.yaml plus one entrypoint file
// (entrypointPath -> entrypointContent) and tags the commit.
func tagRelease(t *testing.T, dir, tag, manifest, entrypointPath, entrypointContent string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ManifestFileName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(dir, filepath.FromSlash(entrypointPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(entrypointContent), 0o644); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, dir, "add", "-A")
	runFixtureGit(t, dir, "commit", "-m", "release "+tag)
	runFixtureGit(t, dir, "tag", tag)
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

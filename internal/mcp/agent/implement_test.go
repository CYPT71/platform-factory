package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runFixtureGitCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// fakeGHOnPath writes a minimal fake `gh` script that responds to `gh
// pr create` with a fake PR URL on stdout, returning its containing
// directory so a test can prepend it to PATH - this exercises
// proposeChange's full commit/push/CreatePR flow without a real GitHub
// call or gh authentication.
func fakeGHOnPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"pr\" ] && [ \"$2\" = \"create\" ]; then\n  echo https://github.com/example/repo/pull/1\n  exit 0\nfi\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestImplementPluginOnlyCreatesAndOpensAPR(t *testing.T) {
	dir := fixtureRepo(t)
	remote := t.TempDir()
	runFixtureGitCommand(t, remote, "init", "--bare", "--initial-branch=main")
	runFixtureGitCommand(t, dir, "remote", "add", "origin", remote)
	t.Setenv("PATH", fakeGHOnPath(t)+":"+os.Getenv("PATH"))

	plan := `{
		"strategy": "plugin_only",
		"reasoning": "Bun can be supported entirely as a new language-adjacent capability plugin.",
		"plugin": {"name": "bun-builder", "action": "create", "description": "Support Bun applications.", "capabilities": ["detect", "build"], "request": ""}
	}`
	client := fakeClient(t, []string{plan})

	result, err := Implement(context.Background(), client, dir, "Add support for Bun")
	if err != nil {
		t.Fatal(err)
	}
	if result.Strategy != "plugin_only" || result.Plugin == nil || !result.Plugin.Success {
		t.Fatalf("result=%+v", result)
	}
	if result.Core != nil {
		t.Fatalf("expected no core step, got %+v", result.Core)
	}
	if result.PullRequest == nil || *result.PullRequest == "" {
		t.Fatalf("expected a successful plugin-only change to open a PR, result=%+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins", "bun-builder", "plugin.json")); err != nil {
		t.Fatalf("expected the plugin to have been scaffolded: %v", err)
	}
}

func TestClassifyRejectsAnUnrecognizedStrategy(t *testing.T) {
	client := fakeClient(t, []string{`{"strategy": "rewrite_everything", "reasoning": "no"}`})
	if _, err := classify(context.Background(), client, "do something wild"); err == nil {
		t.Fatal("expected an error for an unrecognized strategy")
	}
}

func TestClassifyRejectsANonJSONReply(t *testing.T) {
	client := fakeClient(t, []string{"I would suggest a plugin."})
	if _, err := classify(context.Background(), client, "add bun support"); err == nil {
		t.Fatal("expected an error for a non-JSON reply")
	}
}

func TestSlugifyProducesAShortLowercaseSlug(t *testing.T) {
	got := slugify(&PluginStep{Name: "Bun Builder!!"}, "ignored")
	if got != "bun-builder" {
		t.Fatalf("got=%q", got)
	}
	got = slugify(nil, "Add support for Bun applications, please")
	if got != "add-support-for-bun-applications-please"[:len(got)] {
		t.Fatalf("got=%q", got)
	}
	if len(got) > 40 {
		t.Fatalf("expected a bounded slug length, got %d chars: %q", len(got), got)
	}
}

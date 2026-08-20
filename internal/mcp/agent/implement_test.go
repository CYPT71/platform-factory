package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestImplementPropagatesAClassifyError covers Implement's own
// classify() error branch - TestClassifyRejectsAnUnrecognizedStrategy/
// TestClassifyRejectsANonJSONReply exercise classify directly, never
// through Implement.
func TestImplementPropagatesAClassifyError(t *testing.T) {
	dir := fixtureRepo(t)
	client := fakeClient(t, []string{"not json at all"})
	_, err := Implement(context.Background(), client, dir, "add something")
	if err == nil {
		t.Fatal("expected classify's error to propagate out of Implement")
	}
}

// TestImplementPropagatesAPrepareBranchFailure covers Implement's own
// PrepareBranch error branch: PrepareBranch requires a clean working
// tree, so a dirty fixtureRepo (an untracked file, never committed)
// makes it fail before any plugin/core work starts.
func TestImplementPropagatesAPrepareBranchFailure(t *testing.T) {
	dir := fixtureRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := `{"strategy": "plugin_only", "reasoning": "x", "plugin": {"name": "widget", "action": "create", "description": "d", "capabilities": ["detect"], "request": ""}}`
	client := fakeClient(t, []string{plan})
	_, err := Implement(context.Background(), client, dir, "add widget")
	if err == nil || !strings.Contains(err.Error(), "prepare branch:") {
		t.Fatalf("err=%v", err)
	}
}

// TestImplementCreatesAPluginThenAppliesAFollowUpModifyRequest covers
// the nested ModifyPlugin call inside Implement's "create" branch
// (plan.Plugin.Request != "") - TestImplementPluginOnlyCreatesAndOpensAPR
// uses an empty Request, which skips this block entirely and only
// exercises the immediate-success path below it.
func TestImplementCreatesAPluginThenAppliesAFollowUpModifyRequest(t *testing.T) {
	dir := fixtureRepo(t)
	remote := t.TempDir()
	runFixtureGitCommand(t, remote, "init", "--bare", "--initial-branch=main")
	runFixtureGitCommand(t, dir, "remote", "add", "origin", remote)
	t.Setenv("PATH", fakeGHOnPath(t)+":"+os.Getenv("PATH"))

	plan := `{
		"strategy": "plugin_only",
		"reasoning": "Bun needs a new plugin with an immediate follow-up tweak.",
		"plugin": {"name": "bun-builder", "action": "create", "description": "Support Bun applications.", "capabilities": ["detect", "build"], "request": "add a note to the README"}
	}`
	edit := `[{"path":"plugins/bun-builder/README.md","content":"# bun-builder\n\nSupport Bun applications.\n\nExtra note.\n"}]`
	client := fakeClient(t, []string{plan, edit})

	result, err := Implement(context.Background(), client, dir, "add Bun support with a README tweak")
	if err != nil {
		t.Fatal(err)
	}
	if result.Plugin == nil || !result.Plugin.Success {
		t.Fatalf("result=%+v", result)
	}
	// FilesEdited must combine both the scaffolded files from Create and
	// the follow-up ModifyPlugin edit.
	foundScaffold, foundEdit := false, false
	for _, f := range result.Plugin.FilesEdited {
		if strings.Contains(f, "plugin.json") {
			foundScaffold = true
		}
		if strings.Contains(f, "README.md") {
			foundEdit = true
		}
	}
	if !foundScaffold || !foundEdit {
		t.Fatalf("expected both scaffolded and follow-up-edited files, got %v", result.Plugin.FilesEdited)
	}
}

// TestImplementPropagatesACreateFailure covers Implement's own
// plugins.Create error branch: creating a plugin whose directory
// already exists fails immediately.
func TestImplementPropagatesACreateFailure(t *testing.T) {
	dir := fixtureRepo(t)
	createFixturePlugin(t, dir, "widget")
	runFixtureGitCommand(t, dir, "add", "-A")
	runFixtureGitCommand(t, dir, "commit", "-m", "add the widget fixture plugin")

	plan := `{"strategy": "plugin_only", "reasoning": "x", "plugin": {"name": "widget", "action": "create", "description": "d", "capabilities": ["detect"], "request": ""}}`
	client := fakeClient(t, []string{plan})
	_, err := Implement(context.Background(), client, dir, "recreate widget")
	if err == nil || !strings.Contains(err.Error(), "create plugin") {
		t.Fatalf("err=%v", err)
	}
}

// TestImplementPropagatesAModifyFailureOnAnExistingPlugin covers
// Implement's "modify" (action != create) branch's own ModifyPlugin
// error propagation - TestImplementModifiesAnExistingPluginWithoutCreatingIt
// only exercises the success case.
func TestImplementPropagatesAModifyFailureOnAnExistingPlugin(t *testing.T) {
	dir := fixtureRepo(t)
	createFixturePlugin(t, dir, "widget")
	runFixtureGitCommand(t, dir, "add", "-A")
	runFixtureGitCommand(t, dir, "commit", "-m", "add the widget fixture plugin")

	plan := `{"strategy": "plugin_only", "reasoning": "x", "plugin": {"name": "widget", "action": "modify", "request": "do something"}}`
	bad := "not json at all"
	client := fakeClient(t, []string{plan, bad, bad, bad})
	_, err := Implement(context.Background(), client, dir, "modify widget")
	if err == nil || !strings.Contains(err.Error(), "modify plugin") {
		t.Fatalf("err=%v", err)
	}
}

// TestImplementPropagatesAProposeChangeFailure covers Implement's own
// "change validated but commit/push/PR failed" branch: a successful
// plugin create with no git remote configured makes proposeChange's own
// Push fail.
func TestImplementPropagatesAProposeChangeFailure(t *testing.T) {
	dir := fixtureRepo(t)
	plan := `{"strategy": "plugin_only", "reasoning": "x", "plugin": {"name": "bun-builder", "action": "create", "description": "d", "capabilities": ["detect"], "request": ""}}`
	client := fakeClient(t, []string{plan})
	_, err := Implement(context.Background(), client, dir, "add bun support")
	if err == nil || !strings.Contains(err.Error(), "change validated but commit/push/PR failed") {
		t.Fatalf("err=%v", err)
	}
}

// TestImplementModifiesAnExistingPluginWithoutCreatingIt covers
// Implement's "action != create" branch (the else at implement.go's own
// plugin block): it goes straight to ModifyPlugin, skipping
// plugins.Create entirely, unlike TestImplementPluginOnlyCreatesAndOpensAPR
// which only exercises the "create" branch.
func TestImplementModifiesAnExistingPluginWithoutCreatingIt(t *testing.T) {
	dir := fixtureRepo(t)
	createFixturePlugin(t, dir, "widget")
	// Implement's PrepareBranch requires a clean working tree, so the
	// plugin fixture's own files must be committed first - unlike
	// TestModifyPluginSucceedsOnFirstValidAttempt, which drives
	// ModifyPlugin directly and never reaches PrepareBranch.
	runFixtureGitCommand(t, dir, "add", "-A")
	runFixtureGitCommand(t, dir, "commit", "-m", "add the widget fixture plugin")
	remote := t.TempDir()
	runFixtureGitCommand(t, remote, "init", "--bare", "--initial-branch=main")
	runFixtureGitCommand(t, dir, "remote", "add", "origin", remote)
	t.Setenv("PATH", fakeGHOnPath(t)+":"+os.Getenv("PATH"))

	plan := `{
		"strategy": "plugin_only",
		"reasoning": "The widget plugin already exists; just needs docs.",
		"plugin": {"name": "widget", "action": "modify", "request": "add a README"}
	}`
	edit := `[{"path":"plugins/widget/README.md","content":"# widget\n\nUpdated.\n"}]`
	client := fakeClient(t, []string{plan, edit})

	result, err := Implement(context.Background(), client, dir, "document the widget plugin")
	if err != nil {
		t.Fatal(err)
	}
	if result.Plugin == nil || !result.Plugin.Success {
		t.Fatalf("result=%+v", result)
	}
	if result.PullRequest == nil || *result.PullRequest == "" {
		t.Fatalf("expected a PR to be opened, result=%+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins", "widget", "README.md")); err != nil {
		t.Fatalf("expected the modify request to have written README.md: %v", err)
	}
}

// TestImplementCoreOnlyPropagatesAPatchCoreFailure covers Implement's
// "plan.Core != nil" branch and its error-wrapping ("patch core: %w") -
// nothing in this file previously drove a core_only or core_and_plugin
// plan through Implement at all. PatchCore's own SelfCheck step (`go
// test ./internal/archtest/...`) cannot succeed against fixtureRepo (it
// has no internal/archtest package to run), so every attempt here
// exhausts maxIterations and PatchCore itself fails - which is exactly
// what proves Implement's error-propagation path, without needing a
// real archtest-passing fixture.
func TestImplementCoreOnlyPropagatesAPatchCoreFailure(t *testing.T) {
	dir := fixtureRepo(t)
	plan := `{
		"strategy": "core_only",
		"reasoning": "This needs a new capability the plugin protocol does not expose.",
		"core": {"reason": "new extension point", "allowed_paths": ["internal/example/x.go"], "request": "add a function"}
	}`
	edit := `[{"path":"internal/example/x.go","content":"package example\n"}]`
	client := fakeClient(t, []string{plan, edit, edit, edit})

	_, err := Implement(context.Background(), client, dir, "add a new internal capability")
	if err == nil || !strings.Contains(err.Error(), "patch core:") {
		t.Fatalf("err=%v", err)
	}
}

// TestBuildPRBodyIncludesEveryOptionalSection covers buildPRBody's
// branches TestImplementPluginOnlyCreatesAndOpensAPR's plugin-only,
// no-explanation, no-core-reason plan never reaches: a bug-report-shaped
// Explanation (both root cause and fix), a Core step with its own
// Reason and AllowedPaths, and FilesEdited on both the Plugin and Core
// halves of the result.
func TestBuildPRBodyIncludesEveryOptionalSection(t *testing.T) {
	plan := Plan{
		Strategy:  "core_and_plugin",
		Reasoning: "Needs both a plugin change and a new core extension point.",
		Explanation: &Explanation{
			RootCause: "The plugin protocol has no hook for this.",
			Fix:       "Add the hook and wire the plugin to it.",
		},
		Core: &CoreStep{Reason: "expose the new hook", AllowedPaths: []string{"internal/example/x.go"}},
	}
	result := ImplementResult{
		Strategy: "core_and_plugin",
		Plugin:   &ModifyResult{Success: true, Iterations: 2, FilesEdited: []string{"plugins/widget/main.go"}},
		Core:     &ModifyResult{Success: true, Iterations: 1, FilesEdited: []string{"internal/example/x.go"}},
	}
	body := buildPRBody("add a new hook", plan, result)
	for _, want := range []string{
		"## Bug: Root Cause", "The plugin protocol has no hook for this.",
		"## Fix", "Add the hook and wire the plugin to it.",
		"## Why A Core Change Was Needed", "expose the new hook",
		"## Plugin Changes", "plugins/widget/main.go",
		"## Core Changes", "internal/example/x.go",
		"pf_plugin_validate: 2 iteration(s), success=true",
		"go test ./internal/archtest/... (self-check): 1 iteration(s), success=true",
		"Core changes were confined to: internal/example/x.go.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("PR body missing %q: %s", want, body)
		}
	}
}

// TestBuildPRBodyReportsMissingExplanationFields covers the
// "(not provided)" fallback for an Explanation present but missing one
// of its two fields - a case TestBuildPRBodyIncludesEveryOptionalSection
// (both fields populated) does not reach.
func TestBuildPRBodyReportsMissingExplanationFields(t *testing.T) {
	plan := Plan{Explanation: &Explanation{RootCause: "only the cause is known"}}
	body := buildPRBody("fix the bug", plan, ImplementResult{})
	if !strings.Contains(body, "only the cause is known") {
		t.Fatalf("body=%s", body)
	}
	if !strings.Contains(body, "## Fix\n(not provided)") {
		t.Fatalf("expected a (not provided) fallback for the missing Fix, body=%s", body)
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

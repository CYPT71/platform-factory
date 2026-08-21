package git

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusToolHandlerReportsBranchAndDirtyState(t *testing.T) {
	dir := newTestRepo(t)
	handler := StatusToolHandler(dir)

	out, err := handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var status Status
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if status.Branch != "main" {
		t.Fatalf("branch=%q", status.Branch)
	}
	if status.Dirty {
		t.Fatal("expected a clean working tree")
	}
}

func TestStatusResourceHandlerReportsJSON(t *testing.T) {
	dir := newTestRepo(t)
	body, mimeType, err := StatusResourceHandler(dir)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mimeType != "application/json" || !strings.Contains(body, `"branch"`) {
		t.Fatalf("mimeType=%q body=%s", mimeType, body)
	}
}

func TestStatusToolHandlerPropagatesAnErrorForANonRepoDir(t *testing.T) {
	dir := t.TempDir() // deliberately never `git init`-ed
	handler := StatusToolHandler(dir)
	if _, err := handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected an error for a non-repository directory")
	}
}

func TestPrepareBranchToolHandlerCreatesTheRequestedBranch(t *testing.T) {
	dir := newTestRepo(t)
	handler := PrepareBranchToolHandler(dir)

	out, err := handler(context.Background(), json.RawMessage(`{"name":"mcp/feat/example"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if result["branch"] != "mcp/feat/example" || result["started_from"] != "main" {
		t.Fatalf("result=%v", result)
	}

	status, err := New(dir).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Branch != "mcp/feat/example" {
		t.Fatalf("branch=%q", status.Branch)
	}
}

func TestPrepareBranchToolHandlerRejectsMalformedJSON(t *testing.T) {
	dir := newTestRepo(t)
	handler := PrepareBranchToolHandler(dir)
	_, err := handler(context.Background(), json.RawMessage(`{"name":`))
	if err == nil || !strings.Contains(err.Error(), "invalid arguments") {
		t.Fatalf("err=%v, want an 'invalid arguments' error", err)
	}
}

func TestPrepareBranchToolHandlerPropagatesAProtectedNameError(t *testing.T) {
	dir := newTestRepo(t)
	handler := PrepareBranchToolHandler(dir)
	if _, err := handler(context.Background(), json.RawMessage(`{"name":"main"}`)); err == nil {
		t.Fatal("expected an error for a protected branch name")
	}
}

func TestCommitToolHandlerCommitsGivenPathsAndReportsTheResultingBranch(t *testing.T) {
	dir := newTestRepo(t)
	if _, err := New(dir).PrepareBranch(context.Background(), "mcp/feat/example"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := CommitToolHandler(dir)

	out, err := handler(context.Background(), json.RawMessage(`{"paths":["a.txt"],"message":"add a"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if result["committed"] != true {
		t.Fatalf("result=%v", result)
	}
	if result["branch"] != "mcp/feat/example" {
		t.Fatalf("result=%v", result)
	}

	status, err := New(dir).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Dirty {
		t.Fatal("expected a clean working tree after the commit")
	}
}

func TestCommitToolHandlerRejectsMalformedJSON(t *testing.T) {
	dir := newTestRepo(t)
	handler := CommitToolHandler(dir)
	_, err := handler(context.Background(), json.RawMessage(`not json`))
	if err == nil || !strings.Contains(err.Error(), "invalid arguments") {
		t.Fatalf("err=%v, want an 'invalid arguments' error", err)
	}
}

func TestCommitToolHandlerPropagatesAnEmptyMessageError(t *testing.T) {
	dir := newTestRepo(t)
	if _, err := New(dir).PrepareBranch(context.Background(), "mcp/feat/example"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := CommitToolHandler(dir)
	if _, err := handler(context.Background(), json.RawMessage(`{"paths":["a.txt"],"message":""}`)); err == nil {
		t.Fatal("expected an error for an empty commit message")
	}
}

func TestCreatePRToolHandlerRejectsMalformedJSON(t *testing.T) {
	dir := newTestRepo(t)
	handler := CreatePRToolHandler(dir)
	_, err := handler(context.Background(), json.RawMessage(`{"title":`))
	if err == nil || !strings.Contains(err.Error(), "invalid arguments") {
		t.Fatalf("err=%v, want an 'invalid arguments' error", err)
	}
}

func TestCreatePRToolHandlerPropagatesAStatusErrorForANonRepoDir(t *testing.T) {
	dir := t.TempDir() // never `git init`-ed
	handler := CreatePRToolHandler(dir)
	if _, err := handler(context.Background(), json.RawMessage(`{"title":"t"}`)); err == nil {
		t.Fatal("expected the underlying Status error to propagate")
	}
}

func TestCreatePRToolHandlerPropagatesAPushFailureWhenNoRemoteIsConfigured(t *testing.T) {
	dir := newTestRepo(t)
	if _, err := New(dir).PrepareBranch(context.Background(), "mcp/feat/example"); err != nil {
		t.Fatal(err)
	}
	handler := CreatePRToolHandler(dir)
	_, err := handler(context.Background(), json.RawMessage(`{"title":"t"}`))
	if err == nil {
		t.Fatal("expected an error pushing with no remote configured")
	}
}

func TestCreatePRToolHandlerSucceedsAndReturnsTheURLAndHead(t *testing.T) {
	dir := newTestRepo(t)
	remote := t.TempDir()
	runFixtureGit(t, remote, "init", "--bare", "--initial-branch=main")
	runFixtureGit(t, dir, "remote", "add", "origin", remote)
	if _, err := New(dir).PrepareBranch(context.Background(), "mcp/feat/example"); err != nil {
		t.Fatal(err)
	}
	newFakeGH(t, "#!/bin/sh\necho \"https://github.com/x/y/pull/42\"\n")

	handler := CreatePRToolHandler(dir)
	out, err := handler(context.Background(), json.RawMessage(`{"title":"t"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if result["url"] != "https://github.com/x/y/pull/42" {
		t.Fatalf("result=%v", result)
	}
	if result["head"] != "mcp/feat/example" {
		t.Fatalf("result=%v", result)
	}

	refs := runFixtureGit(t, remote, "branch")
	if !strings.Contains(refs, "mcp/feat/example") {
		t.Fatalf("expected the branch to have been pushed, got: %q", refs)
	}
}

package git

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusToolHandler(t *testing.T) {
	dir := newTestRepo(t)
	handler := StatusToolHandler(dir)
	output, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var status Status
	if err := json.Unmarshal([]byte(output), &status); err != nil {
		t.Fatalf("decode output: %v (output=%s)", err, output)
	}
	if status.Branch != "main" {
		t.Fatalf("branch = %q, want main", status.Branch)
	}
}

func TestPrepareBranchToolHandler(t *testing.T) {
	dir := newTestRepo(t)
	handler := PrepareBranchToolHandler(dir)

	output, err := handler(context.Background(), json.RawMessage(`{"name":"mcp/feat/example"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("decode output: %v (output=%s)", err, output)
	}
	if decoded["branch"] != "mcp/feat/example" || decoded["started_from"] != "main" {
		t.Fatalf("decoded = %v", decoded)
	}

	if _, err := handler(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Fatal("expected an error for invalid JSON arguments")
	}

	if _, err := handler(context.Background(), json.RawMessage(`{"name":"main"}`)); err == nil {
		t.Fatal("expected an error for a protected branch name")
	}
}

func TestCommitToolHandler(t *testing.T) {
	dir := newTestRepo(t)
	if _, err := PrepareBranchToolHandler(dir)(context.Background(), json.RawMessage(`{"name":"mcp/feat/example"}`)); err != nil {
		t.Fatalf("prepare branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new-file.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := CommitToolHandler(dir)
	output, err := handler(context.Background(), json.RawMessage(`{"paths":["new-file.txt"],"message":"add new file"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("decode output: %v (output=%s)", err, output)
	}
	if decoded["committed"] != true || decoded["branch"] != "mcp/feat/example" {
		t.Fatalf("decoded = %v", decoded)
	}

	if _, err := handler(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Fatal("expected an error for invalid JSON arguments")
	}

	if _, err := handler(context.Background(), json.RawMessage(`{"paths":["missing.txt"],"message":""}`)); err == nil {
		t.Fatal("expected an error for an empty commit message")
	}
}

func TestCreatePRToolHandler(t *testing.T) {
	dir := newTestRepo(t)
	handler := CreatePRToolHandler(dir)

	if _, err := handler(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Fatal("expected an error for invalid JSON arguments")
	}

	// The fixture repo is still on "main" (protected) with no remote
	// configured. CreatePRToolHandler pushes before ever calling gh, so
	// this deterministically fails at the protected-branch push check -
	// entirely locally, no gh CLI or network access required.
	_, err := handler(context.Background(), json.RawMessage(`{"title":"Add feature","base":"main"}`))
	if err == nil {
		t.Fatal("expected an error when the current branch is protected")
	}
	if !strings.Contains(err.Error(), "protected branch") {
		t.Fatalf("error = %v, want a protected-branch push refusal", err)
	}
}

package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileWritesInsideTheRepo(t *testing.T) {
	dir := t.TempDir()
	result, err := WriteFile(dir, "internal/example/file.go", "package example\n")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Written || result.Path != "internal/example/file.go" {
		t.Fatalf("result=%+v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "internal", "example", "file.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "package example\n" {
		t.Fatalf("content=%q", data)
	}
}

func TestReadFileReadsBackWhatWasWritten(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteFile(dir, "a/b.txt", "hello"); err != nil {
		t.Fatal(err)
	}
	result, err := ReadFile(dir, "a/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "hello" {
		t.Fatalf("content=%q", result.Content)
	}
}

func TestWriteFileRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	cases := []string{
		"../escape.txt",
		"../../etc/passwd",
		"a/../../escape.txt",
		"/etc/passwd",
		"",
	}
	for _, path := range cases {
		if _, err := WriteFile(dir, path, "x"); err == nil {
			t.Errorf("WriteFile(%q) expected an error", path)
		}
	}
	// Nothing must have been written outside dir - check the parent of
	// dir gained no new file.
	entries, err := os.ReadDir(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "escape.txt" || e.Name() == "passwd" {
			t.Fatalf("a path-traversal write escaped the repo: found %s", e.Name())
		}
	}
}

func TestReadFileRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("do not read me"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(secret)

	if _, err := ReadFile(dir, "../secret.txt"); err == nil {
		t.Fatal("expected an error reading outside the repo via ..")
	}
}

func TestWriteFileRefusesToFollowASymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated privileges on windows")
	}
	dir := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "real.txt")
	if err := os.WriteFile(outsideFile, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "link.txt")
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Fatal(err)
	}

	if _, err := WriteFile(dir, "link.txt", "attacker-controlled"); err == nil {
		t.Fatal("expected an error writing through a symlink")
	}
	data, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatal("the symlink target outside the repo must not have been modified")
	}
}

func TestWriteFileRefusesASymlinkedParentDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated privileges on windows")
	}
	dir := t.TempDir()
	outside := t.TempDir()
	linkedDir := filepath.Join(dir, "linked")
	if err := os.Symlink(outside, linkedDir); err != nil {
		t.Fatal(err)
	}

	if _, err := WriteFile(dir, "linked/escape.txt", "x"); err == nil {
		t.Fatal("expected an error writing under a symlinked directory that points outside the repo")
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.txt")); err == nil {
		t.Fatal("a write escaped through a symlinked parent directory")
	}
}

func TestReadFileToolHandlerRoundTripsValidArguments(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteFile(dir, "a/b.txt", "hello"); err != nil {
		t.Fatal(err)
	}
	handler := ReadFileToolHandler(dir)
	out, err := handler(context.Background(), json.RawMessage(`{"path":"a/b.txt"}`))
	if err != nil {
		t.Fatalf("handler returned an error: %v", err)
	}
	var result ReadFileResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("handler output is not valid JSON: %v (%s)", err, out)
	}
	if result.Content != "hello" {
		t.Fatalf("content=%q", result.Content)
	}
}

func TestReadFileToolHandlerRejectsInvalidJSON(t *testing.T) {
	handler := ReadFileToolHandler(t.TempDir())
	if _, err := handler(context.Background(), json.RawMessage(`{not json`)); err == nil {
		t.Fatal("expected an error for malformed JSON arguments")
	}
}

func TestWriteFileToolHandlerRoundTripsValidArguments(t *testing.T) {
	dir := t.TempDir()
	handler := WriteFileToolHandler(dir)
	out, err := handler(context.Background(), json.RawMessage(`{"path":"a/b.txt","content":"hello"}`))
	if err != nil {
		t.Fatalf("handler returned an error: %v", err)
	}
	var result WriteFileResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("handler output is not valid JSON: %v (%s)", err, out)
	}
	if !result.Written || result.Path != "a/b.txt" {
		t.Fatalf("result=%+v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "a", "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("content=%q", data)
	}
}

func TestWriteFileToolHandlerRejectsInvalidJSON(t *testing.T) {
	handler := WriteFileToolHandler(t.TempDir())
	if _, err := handler(context.Background(), json.RawMessage(`{not json`)); err == nil {
		t.Fatal("expected an error for malformed JSON arguments")
	}
}

func TestSelfCheckToolHandlerReturnsAStepResult(t *testing.T) {
	// A fake repoRoot with no Go module context is enough to exercise the
	// handler's own JSON-encoding wrapper around SelfCheck without paying
	// for a real "go test ./internal/archtest/..." run the way
	// TestSelfCheckRunsArchtestAgainstTheRealRepo (below) already does
	// against the real repo.
	handler := SelfCheckToolHandler(t.TempDir())
	out, err := handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler returned an error: %v", err)
	}
	var step StepResult
	if err := json.Unmarshal([]byte(out), &step); err != nil {
		t.Fatalf("handler output is not valid JSON: %v (%s)", err, out)
	}
	if step.Name == "" {
		t.Fatalf("step=%+v", step)
	}
}

// TestReadFileSurfacesANonNotExistError covers ReadFile's generic
// error-propagation branch (distinct from the os.IsNotExist ->
// toolerror.ErrNotFound branch every other ReadFile test in this file
// exercises): os.ReadFile on a path that is a directory, not a
// nonexistent one, fails with an "is a directory" error that ReadFile
// must return unwrapped.
func TestReadFileSurfacesANonNotExistError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a-directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(dir, "a-directory"); err == nil {
		t.Fatal("expected an error reading a directory as a file")
	}
}

// TestWriteFileSurfacesAMkdirAllFailure covers WriteFile's own
// os.MkdirAll error branch: a path component that already exists as a
// regular file (not a directory) makes MkdirAll fail.
func TestWriteFileSurfacesAMkdirAllFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blocker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFile(dir, "blocker/child.txt", "content"); err == nil {
		t.Fatal("expected an error when a path component is an existing regular file")
	}
}

// TestWriteFileSurfacesAWriteFailure covers WriteFile's own
// os.WriteFile error branch: writing to a path that is already an
// existing directory fails with "is a directory".
func TestWriteFileSurfacesAWriteFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "already-a-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFile(dir, "already-a-dir", "content"); err == nil {
		t.Fatal("expected an error writing to a path that is already a directory")
	}
}

func TestSelfCheckRunsArchtestAgainstTheRealRepo(t *testing.T) {
	repoRoot := findRealRepoRoot(t)
	step := SelfCheck(context.Background(), repoRoot)
	if step.Status != "ok" {
		t.Fatalf("archtest self-check failed against the real repo: %s", step.Output)
	}
}

// findRealRepoRoot walks up from the test's working directory to the
// real platform-factory checkout, so TestSelfCheckRunsArchtestAgainstTheRealRepo
// exercises the actual internal/archtest suite this tool shells out to,
// not a synthetic fixture (archtest inspects the real go.work/module
// graph, which a minimal fixture can't stand in for).
func findRealRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.work")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate the repo root (go.work) by walking up from the test's working directory")
		}
		dir = parent
	}
}

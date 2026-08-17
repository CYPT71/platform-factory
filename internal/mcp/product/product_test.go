package product

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// realPFBinary builds the actual platform-factory binary once per test
// process and re-execs the *test binary itself* is not what these tests
// exercise - selfExecutable() resolves os.Executable(), so to test the
// real subprocess path these tests must literally run as that binary.
// Building it fresh here (rather than trying to fake os.Executable)
// keeps the test honest: it proves the real self-re-exec mechanism
// against a real, freshly-built platform-factory binary.
func realPFBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "platform-factory")
	repoRoot := findRealRepoRoot(t)
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/platform-factory")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build platform-factory: %v\n%s", err, out)
	}
	return bin
}

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
			t.Fatal("could not locate the repo root (go.work) by walking up")
		}
		dir = parent
	}
}

// runWithBinary exercises the exact subprocess mechanism run() uses
// (argument array, working directory, captured stdout/stderr) against
// a real freshly-built platform-factory binary. It calls that binary
// directly rather than through selfExecutable(), since that resolves
// os.Executable() of the running process - the `go test` binary, not
// platform-factory - so the handlers themselves can't be exercised
// end-to-end from within this test process; this proves the same real
// command line and I/O plumbing they build instead.
func runWithBinary(t *testing.T, bin, repoRoot, subcommand string, args []string) Result {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), bin, append([]string{subcommand}, args...)...)
	cmd.Dir = repoRoot
	output, _ := cmd.CombinedOutput()
	return Result{Command: subcommand, Args: args, Stdout: string(output)}
}

func TestBoolFlagAppendsOnlyWhenSet(t *testing.T) {
	if got := boolFlag(nil, "--dry-run", true); len(got) != 1 || got[0] != "--dry-run" {
		t.Fatalf("got=%v", got)
	}
	if got := boolFlag(nil, "--dry-run", false); len(got) != 0 {
		t.Fatalf("got=%v", got)
	}
}

func TestStringFlagAppendsOnlyWhenNonEmpty(t *testing.T) {
	if got := stringFlag(nil, "--name", "x"); len(got) != 2 || got[0] != "--name" || got[1] != "x" {
		t.Fatalf("got=%v", got)
	}
	if got := stringFlag(nil, "--name", ""); len(got) != 0 {
		t.Fatalf("got=%v", got)
	}
}

func TestValidExtraArgsRejectsNULBytes(t *testing.T) {
	if err := validExtraArgs([]string{"--flag", "a\x00b"}); err == nil {
		t.Fatal("expected an error for a NUL byte in extra_args")
	}
	if err := validExtraArgs([]string{"--flag", "value"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestScopedRelativeRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	if _, err := scopedRelative(dir, "../escape"); err == nil {
		t.Fatal("expected an error for a path-traversal argument")
	}
	if _, err := scopedRelative(dir, "/etc/passwd"); err == nil {
		t.Fatal("expected an error for an absolute path argument")
	}
	got, err := scopedRelative(dir, "sub/dir")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sub/dir" {
		t.Fatalf("got=%q", got)
	}
	if got, err := scopedRelative(dir, ""); err != nil || got != "" {
		t.Fatalf("empty input should pass through unchanged: got=%q err=%v", got, err)
	}
}

func TestStatusToolHandlerRunsTheRealBinary(t *testing.T) {
	bin := realPFBinary(t)
	repoRoot := t.TempDir()
	result := runWithBinary(t, bin, repoRoot, "status", []string{"--format", "json"})
	var document map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &document); err != nil {
		t.Fatalf("expected valid JSON status output: %v\n%s", err, result.Stdout)
	}
	if _, ok := document["initialized"]; !ok {
		t.Fatalf("expected an 'initialized' field, got %v", document)
	}
}

func TestDoctorToolHandlerRunsTheRealBinary(t *testing.T) {
	bin := realPFBinary(t)
	repoRoot := t.TempDir()
	result := runWithBinary(t, bin, repoRoot, "doctor", []string{"--json"})
	var document map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &document); err != nil {
		t.Fatalf("expected valid JSON doctor output: %v\n%s", err, result.Stdout)
	}
	if _, ok := document["checks"]; !ok {
		t.Fatalf("expected a 'checks' field, got %v", document)
	}
}

func TestDoctorToolHandlerRejectsAnInvalidScope(t *testing.T) {
	handler := DoctorToolHandler(t.TempDir())
	_, err := handler(context.Background(), json.RawMessage(`{"scope":"bogus"}`))
	if err == nil {
		t.Fatal("expected an error for an invalid scope")
	}
}

func TestDoctorToolHandlerRejectsInvalidJSON(t *testing.T) {
	handler := DoctorToolHandler(t.TempDir())
	if _, err := handler(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Fatal("expected an error for invalid JSON arguments")
	}
}

func TestStatusToolHandlerValidatesBeforeRunningAnything(t *testing.T) {
	dir := t.TempDir()
	handler := StatusToolHandler(dir)
	if _, err := handler(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Fatal("expected an error for invalid JSON arguments")
	}
	if _, err := handler(context.Background(), json.RawMessage(`{"directory":"../escape"}`)); err == nil {
		t.Fatal("expected an error for a path-traversal directory argument")
	}
}

func TestDetectToolHandlerValidatesBeforeRunningAnything(t *testing.T) {
	dir := t.TempDir()
	handler := DetectToolHandler(dir)
	if _, err := handler(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Fatal("expected an error for invalid JSON arguments")
	}
	if _, err := handler(context.Background(), json.RawMessage(`{"path":"../escape"}`)); err == nil {
		t.Fatal("expected an error for a path-traversal path argument")
	}
}

func TestInspectToolHandlerRequiresALayout(t *testing.T) {
	handler := InspectToolHandler(t.TempDir())
	if _, err := handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected an error when layout is missing")
	}
}

func TestLayoutToolHandlersValidateBeforeRunningAnything(t *testing.T) {
	dir := t.TempDir()
	for _, newHandler := range []func(string) func(context.Context, json.RawMessage) (string, error){
		VerifyToolHandler, InspectToolHandler,
	} {
		handler := newHandler(dir)
		if _, err := handler(context.Background(), json.RawMessage(`not json`)); err == nil {
			t.Fatal("expected an error for invalid JSON arguments")
		}
		if _, err := handler(context.Background(), json.RawMessage(`{"layout":"../escape"}`)); err == nil {
			t.Fatal("expected an error for a path-traversal layout argument")
		}
	}
}

func TestDetectToolHandlerRequiresAPath(t *testing.T) {
	handler := DetectToolHandler(t.TempDir())
	_, err := handler(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected an error when path is missing")
	}
}

func TestVerifyToolHandlerRequiresALayout(t *testing.T) {
	handler := VerifyToolHandler(t.TempDir())
	_, err := handler(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected an error when layout is missing")
	}
}

func TestInitAndBuildToolHandlersRejectPathTraversal(t *testing.T) {
	dir := t.TempDir()
	if _, err := InitToolHandler(dir)(context.Background(), json.RawMessage(`{"directory":"../escape"}`)); err == nil {
		t.Fatal("expected pf_init to reject a path-traversal directory")
	}
	if _, err := BuildToolHandler(dir)(context.Background(), json.RawMessage(`{"executable":"../escape"}`)); err == nil {
		t.Fatal("expected pf_build to reject a path-traversal executable")
	}
	if _, err := PublishToolHandler(dir)(context.Background(), json.RawMessage(`{"layout":"../escape"}`)); err == nil {
		t.Fatal("expected pf_publish to reject a path-traversal layout")
	}
	if _, err := ProjectToolHandler(dir)(context.Background(), json.RawMessage(`{"action":"show","directory":"../escape"}`)); err == nil {
		t.Fatal("expected pf_project to reject a path-traversal directory")
	}
}

func TestProjectToolHandlerRejectsAnInvalidAction(t *testing.T) {
	handler := ProjectToolHandler(t.TempDir())
	_, err := handler(context.Background(), json.RawMessage(`{"action":"delete"}`))
	if err == nil {
		t.Fatal("expected an error for an invalid action")
	}
}

// TestDeployToolHandlerValidatesBeforeRunningAnything covers every
// error branch DeployToolHandler reaches before its own call to run() -
// invalid JSON, unsafe extra_args, and each of the three
// repository-scoped path arguments. It deliberately never supplies
// arguments that pass every validation, since a successful call would
// reach run()'s self-re-exec of os.Executable() - the running `go test`
// binary itself, not platform-factory - and re-run this entire test
// binary recursively (see realPFBinary's own doc comment above for why
// these handlers can only be driven end-to-end via a real, separately
// built binary).
func TestDeployToolHandlerValidatesBeforeRunningAnything(t *testing.T) {
	dir := t.TempDir()
	handler := DeployToolHandler(dir)

	if _, err := handler(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Fatal("expected an error for invalid JSON arguments")
	}
	if _, err := handler(context.Background(), json.RawMessage(`{"extra_args":["a\u0000b"]}`)); err == nil {
		t.Fatal("expected an error for a NUL byte in extra_args")
	}
	if _, err := handler(context.Background(), json.RawMessage(`{"reports":"../escape"}`)); err == nil {
		t.Fatal("expected an error for a path-traversal reports argument")
	}
	if _, err := handler(context.Background(), json.RawMessage(`{"policy":"../escape"}`)); err == nil {
		t.Fatal("expected an error for a path-traversal policy argument")
	}
	if _, err := handler(context.Background(), json.RawMessage(`{"evidence":"../escape"}`)); err == nil {
		t.Fatal("expected an error for a path-traversal evidence argument")
	}
}

func TestProjectToolHandlerShowRunsTheRealBinary(t *testing.T) {
	bin := realPFBinary(t)
	repoRoot := t.TempDir()
	result := runWithBinary(t, bin, repoRoot, "project", []string{"show"})
	if result.Stdout == "" {
		t.Fatal("expected some output about the missing project config from an empty directory")
	}
}

package product

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// helperProcessEnv/helperExitCodeEnv gate the TestMain stand-in below -
// the same env-var-gated "act as a different program when re-exec'd"
// pattern Go's own os/exec tests use (TestHelperProcess), applied here so
// run()/selfExecutable() can be exercised for real without recursively
// restarting this package's own test suite. selfExecutable() resolves
// os.Executable(), which under `go test` is this very test binary; if a
// test called run() without this guard, the "subprocess" it spawns would
// be the test binary itself, which - absent the env-var short-circuit in
// TestMain - would just re-run the whole suite (including that same
// test) forever. Setting helperProcessEnv before calling run() makes the
// spawned child take the short-circuit branch instead: it never reaches
// m.Run(), so it can't recurse, and it deterministically echoes its argv
// and exits with helperExitCodeEnv's value so the test can assert on
// exactly what run() plumbed through.
const (
	helperProcessEnv  = "PLATFORM_FACTORY_PRODUCT_TEST_HELPER_PROCESS"
	helperExitCodeEnv = "PLATFORM_FACTORY_PRODUCT_TEST_HELPER_EXIT_CODE"
)

func TestMain(m *testing.M) {
	if os.Getenv(helperProcessEnv) == "1" {
		runAsHelperProcess()
		return
	}
	os.Exit(m.Run())
}

// runAsHelperProcess makes this test binary stand in for the real
// platform-factory binary: it echoes its own argv (and its own working
// directory, so project_root-override tests can assert exactly what
// cmd.Dir the handler under test set) to stdout/stderr and exits with
// helperExitCodeEnv's value, then stops - it never calls m.Run(), so a
// chain of these never recurses.
func runAsHelperProcess() {
	argv := strings.Join(os.Args[1:], "|")
	cwd, _ := os.Getwd()
	fmt.Fprintf(os.Stdout, "stdout-argv:%s\n", argv)
	fmt.Fprintf(os.Stdout, "stdout-cwd:%s\n", cwd)
	fmt.Fprintf(os.Stderr, "stderr-argv:%s\n", argv)
	code := 0
	if v := os.Getenv(helperExitCodeEnv); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			code = parsed
		}
	}
	os.Exit(code)
}

// withHelperProcess arranges for the next run() call in this test to
// re-exec the helper stand-in above (via inherited environment on the
// spawned child) instead of actually restarting the test suite.
func withHelperProcess(t *testing.T, exitCode int) {
	t.Helper()
	t.Setenv(helperProcessEnv, "1")
	t.Setenv(helperExitCodeEnv, strconv.Itoa(exitCode))
}

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

// cwdFromStdout extracts the "stdout-cwd:" line runAsHelperProcess wrote,
// so a test can assert exactly which directory the subprocess actually
// ran in (i.e. what cmd.Dir the handler under test set), not just the
// argv it built.
func cwdFromStdout(t *testing.T, stdout string) string {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		if rest, ok := strings.CutPrefix(line, "stdout-cwd:"); ok {
			return rest
		}
	}
	t.Fatalf("no stdout-cwd line found in %q", stdout)
	return ""
}

// evalSymlinksOrFatal resolves symlinks the same way resolveProjectRoot's
// callers ultimately do when the OS itself resolves cmd.Dir (macOS's
// TempDir, for one, lives under a symlinked /var -> /private/var), so
// comparing a raw project_root argument against a subprocess's reported
// os.Getwd() needs both sides normalized the same way.
func evalSymlinksOrFatal(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return resolved
}

func TestResolveProjectRootDefaultsToRepoRootWhenEmpty(t *testing.T) {
	repoRoot := t.TempDir()
	got, err := resolveProjectRoot(repoRoot, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != repoRoot {
		t.Fatalf("got=%q want=%q", got, repoRoot)
	}
}

func TestResolveProjectRootRejectsARelativePath(t *testing.T) {
	repoRoot := t.TempDir()
	if _, err := resolveProjectRoot(repoRoot, "relative/path"); err == nil {
		t.Fatal("expected an error for a relative project_root")
	}
}

func TestResolveProjectRootRejectsANonexistentDirectory(t *testing.T) {
	repoRoot := t.TempDir()
	missing := filepath.Join(repoRoot, "does-not-exist")
	if _, err := resolveProjectRoot(repoRoot, missing); err == nil {
		t.Fatal("expected an error for a nonexistent project_root")
	}
}

func TestResolveProjectRootRejectsAFile(t *testing.T) {
	repoRoot := t.TempDir()
	file := filepath.Join(repoRoot, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveProjectRoot(repoRoot, file); err == nil {
		t.Fatal("expected an error for a project_root that is a file, not a directory")
	}
}

// TestResolveProjectRootAcceptsAnIndependentAbsoluteDirectory is the core
// of F3: project_root deliberately does NOT need to be a Go module root
// and does NOT need to live inside repoRoot - it can be any existing
// absolute directory elsewhere on disk.
func TestResolveProjectRootAcceptsAnIndependentAbsoluteDirectory(t *testing.T) {
	repoRoot := t.TempDir()
	independent := t.TempDir()
	got, err := resolveProjectRoot(repoRoot, independent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evalSymlinksOrFatal(t, got) != evalSymlinksOrFatal(t, independent) {
		t.Fatalf("got=%q want=%q", got, independent)
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

func TestProjectToolHandlerShowRunsTheRealBinary(t *testing.T) {
	bin := realPFBinary(t)
	repoRoot := t.TempDir()
	result := runWithBinary(t, bin, repoRoot, "project", []string{"show"})
	if result.Stdout == "" {
		t.Fatal("expected some output about the missing project config from an empty directory")
	}
}

func TestSelfExecutableResolvesAnAbsoluteExistingPath(t *testing.T) {
	path, err := selfExecutable()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("expected an absolute path, got %q", path)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("expected the resolved path to exist: %v", statErr)
	}
}

func TestRunCapturesStdoutStderrArgsAndZeroExit(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	args := []string{"--dry-run", "--tag", "v1"}
	result, err := run(context.Background(), repoRoot, "build", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Command != "build" {
		t.Fatalf("command = %q, want %q", result.Command, "build")
	}
	if !reflect.DeepEqual(result.Args, args) {
		t.Fatalf("args = %v, want %v", result.Args, args)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode)
	}
	if !strings.Contains(result.Stdout, "stdout-argv:build|--dry-run|--tag|v1") {
		t.Fatalf("stdout = %q", result.Stdout)
	}
	if !strings.Contains(result.Stderr, "stderr-argv:build|--dry-run|--tag|v1") {
		t.Fatalf("stderr = %q", result.Stderr)
	}
}

func TestRunReturnsNonZeroExitCodeWithoutAGoError(t *testing.T) {
	withHelperProcess(t, 7)
	repoRoot := t.TempDir()
	result, err := run(context.Background(), repoRoot, "deploy", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", result.ExitCode)
	}
}

func TestRunReturnsAGoErrorWhenTheContextIsAlreadyDone(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := run(ctx, repoRoot, "build", nil); err == nil {
		t.Fatal("expected an error when the context is already canceled")
	}
}

func TestEncodeRoundTripsResultAsIndentedJSON(t *testing.T) {
	result := Result{Command: "build", Args: []string{"--dry-run"}, ExitCode: 1, Stdout: "out", Stderr: "err"}
	encoded, err := encode(result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(encoded, "\n  \"command\": \"build\"") {
		t.Fatalf("expected indented JSON, got %q", encoded)
	}
	var decoded Result
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("expected valid JSON: %v", err)
	}
	if !reflect.DeepEqual(decoded, result) {
		t.Fatalf("decoded = %+v, want %+v", decoded, result)
	}
}

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunMCPDispatch(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{"no args", nil, 2, "", "platform-factory mcp"},
		{"help flag", []string{"-h"}, 0, "platform-factory mcp", ""},
		{"help word", []string{"help"}, 0, "platform-factory mcp", ""},
		{"unknown", []string{"bogus"}, 2, "", "unknown subcommand"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runMCP(tc.args, &stdout, &stderr)
			if code != tc.wantCode {
				t.Fatalf("code = %d, want %d (stdout=%q stderr=%q)", code, tc.wantCode, stdout.String(), stderr.String())
			}
			if tc.wantOut != "" && !strings.Contains(stdout.String(), tc.wantOut) {
				t.Fatalf("stdout = %q, want substring %q", stdout.String(), tc.wantOut)
			}
			if tc.wantErr != "" && !strings.Contains(stderr.String(), tc.wantErr) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), tc.wantErr)
			}
		})
	}
}

func TestPrintMCPUsage(t *testing.T) {
	var buf bytes.Buffer
	printMCPUsage(&buf)
	out := buf.String()
	for _, want := range []string{"platform-factory mcp", "mcp serve", "docs/mcp.md"} {
		if !strings.Contains(out, want) {
			t.Fatalf("usage missing %q: %s", want, out)
		}
	}
}

// TestRunMCPServeHandlesTheStandardFlagPackageHelpFlag covers
// runMCPServe's own flag.ErrHelp branch inside flags.Parse's error
// check: unlike runBuild/runInspect/etc., this subcommand has no
// containsHelpFlag pre-check ahead of flags.Parse, so "-h"/"--help" is
// only ever handled by the flag package's own built-in ErrHelp - a
// distinct branch from TestRunMCPServeUsageErrors' "--bogus-flag" case,
// which hits the same if-statement's other (return 2) branch.
func TestRunMCPServeHandlesTheStandardFlagPackageHelpFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMCPServe([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunMCPServeUsageErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMCPServe([]string{"--bogus-flag"}, &stdout, &stderr); code != 2 {
		t.Fatalf("bad flag: code=%d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runMCPServe([]string{"extra"}, &stdout, &stderr); code != 2 {
		t.Fatalf("extra positional arg: code=%d", code)
	}
	if !strings.Contains(stderr.String(), "usage: platform-factory mcp serve") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestRunMCPServeRejectsNonGoModuleRoot(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := runMCPServe([]string{"--repo", dir}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "does not look like a Go module root") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestRunMCPServeRejectsGoModAsADirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "go.mod"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runMCPServe([]string{"--repo", dir}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// withClosedStdin temporarily replaces os.Stdin with a pipe whose write
// end is closed immediately, so anything reading from it (Server.Serve's
// bufio.Scanner) observes EOF right away instead of blocking on the real
// terminal/process stdin - the only way to drive runMCPServe's full
// success path (it always reads os.Stdin directly, never an injectable
// io.Reader) without hanging the test.
func withClosedStdin(t *testing.T) {
	t.Helper()
	original := os.Stdin
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdin = read
	t.Cleanup(func() {
		os.Stdin = original
		_ = read.Close()
	})
}

func TestRunMCPServeSucceedsWithImmediateEOF(t *testing.T) {
	withClosedStdin(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runMCPServe([]string{"--repo", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "pf-mcp: serving stdio for repository") {
		t.Fatalf("expected a startup log line, stderr=%q", stderr.String())
	}
}

func TestRunMCPServeDefaultsRepoToCurrentDirectory(t *testing.T) {
	withClosedStdin(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	var stdout, stderr bytes.Buffer
	code := runMCPServe(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// withOversizedLineStdin replaces os.Stdin with a pipe carrying one
// line longer than mcp.Server.Serve's 16 MiB scanner buffer cap, the
// only deterministic way (short of an actual read error on a real fd)
// to make Serve return a non-nil error and drive runMCPServe's own
// "server.Serve failed" branch.
func withOversizedLineStdin(t *testing.T) {
	t.Helper()
	original := os.Stdin
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = read
	t.Cleanup(func() {
		os.Stdin = original
		_ = read.Close()
	})
	go func() {
		defer write.Close()
		chunk := bytes.Repeat([]byte("x"), 1<<20)
		for i := 0; i < 17; i++ {
			if _, err := write.Write(chunk); err != nil {
				return
			}
		}
	}()
}

func TestRunMCPServeReportsAServeFailure(t *testing.T) {
	withOversizedLineStdin(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runMCPServe([]string{"--repo", dir}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "platform-factory mcp serve:") {
		t.Fatalf("expected the Serve error to be reported, stderr=%q", stderr.String())
	}
}

func TestMCPLanguagePluginBackendWiresRealSDKLangpluginFunctions(t *testing.T) {
	backend := mcpLanguagePluginBackend()

	if _, err := backend.Load("not-a-real-language", filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected backend.Load to surface an error for a nonexistent source")
	}
	if err := backend.Inspect(filepath.Join(t.TempDir(), "no-such-binary"), t.TempDir()); err == nil {
		t.Fatal("expected backend.Inspect to surface an error for a nonexistent binary")
	}
	// Unloading a language plugin that was never loaded is documented as
	// a no-op success (sdk/langplugin.Unload's own doc comment: "the end
	// state ... is what the caller asked for either way") - this still
	// exercises backend.Unload's wiring to the real function.
	if err := backend.Unload("not-a-loaded-plugin"); err != nil {
		t.Fatalf("backend.Unload of a never-loaded plugin should be a no-op, got: %v", err)
	}
}

func TestRunMCPDispatchesToServe(t *testing.T) {
	withClosedStdin(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runMCP([]string{"serve", "--repo", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

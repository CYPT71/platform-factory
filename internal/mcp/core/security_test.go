package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// execImportLine and execCallLine are built by concatenation so this
// test file's own source never contains the contiguous substrings the
// real subprocess-execution check (and ci-security.yml's own mirror of
// it) looks for - otherwise this file would need an allowlist entry for
// a pattern it only ever writes into a disposable fixture, never calls.
var execImportLine = "import \"os" + "/exec\"\n"
var execCallLine = "var _ = exec" + ".Command\n"

func writeWorkflow(t *testing.T, repoRoot string, allowlistedPaths ...string) {
	t.Helper()
	var body string
	for _, p := range allowlistedPaths {
		body += "! -path '" + p + "' \\\n"
	}
	content := "steps:\n  - run: |\n      find cmd internal -name '*.go' -type f \\\n" + body +
		"        " + "-exec grep -nE 'os" + "/exec|exec" + `\.Command` + "' {} + || true\n"
	full := filepath.Join(repoRoot, ".github", "workflows", "ci-security.yml")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOSExecAllowlistCheckPassesForAnAllowlistedFile(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "cmd/example/main.go")
	mustWriteFile(t, filepath.Join(dir, "cmd", "example", "main.go"), "package main\n\n"+execImportLine+"\n"+execCallLine)

	step := osExecAllowlistCheck(dir)
	if step.Status != "ok" {
		t.Fatalf("status=%q output=%q", step.Status, step.Output)
	}
}

func TestOSExecAllowlistCheckFailsForAnUnlistedFile(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir) // empty allowlist
	mustWriteFile(t, filepath.Join(dir, "internal", "sneaky", "run.go"), "package sneaky\n\n"+execImportLine+"\n"+execCallLine)

	step := osExecAllowlistCheck(dir)
	if step.Status != "failed" {
		t.Fatalf("status=%q, want failed", step.Status)
	}
	if step.Output == "" {
		t.Fatal("expected the violating file to be named in the output")
	}
}

// tlsBypassFieldLine is built by concatenation for the same
// self-reference reason as execImportLine/execCallLine above: it's the
// exact Go field name the real check looks for, written into a
// disposable fixture, not this test file's own source.
var tlsBypassFieldLine = "var cfg = struct{ Insecure" + "SkipVerify bool }{true}\n"

func TestTLSVerifyBypassCheckFlagsAMatch(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "internal", "x", "tls.go"), "package x\n\n"+tlsBypassFieldLine)

	step := insecureSkipVerifyCheck(dir)
	if step.Status != "failed" {
		t.Fatalf("status=%q", step.Status)
	}
}

func TestTLSVerifyBypassCheckPassesWithoutAMatch(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "internal", "x", "tls.go"), "package x\n")

	step := insecureSkipVerifyCheck(dir)
	if step.Status != "ok" {
		t.Fatalf("status=%q", step.Status)
	}
}

func TestUnfinishedWorkCheckIgnoresReadmeButFlagsOtherFiles(t *testing.T) {
	dir := t.TempDir()
	marker := unfinishedWorkMarkers[0] // built by concatenation - see security.go
	mustWriteFile(t, filepath.Join(dir, "README.md"), "This file may say "+marker+" freely.\n")
	mustWriteFile(t, filepath.Join(dir, "internal", "x", "code.go"), "package x\n\n// "+marker+": finish this\n")

	step := unfinishedWorkCheck(dir)
	if step.Status != "failed" {
		t.Fatalf("status=%q, want failed", step.Status)
	}
	if !strings.Contains(step.Output, "internal/x/code.go") {
		t.Fatalf("output=%q", step.Output)
	}
	if strings.Contains(step.Output, "README.md") {
		t.Fatalf("README.md must be exempt, output=%q", step.Output)
	}
}

func TestUnfinishedWorkCheckPassesOnACleanTree(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "internal", "x", "code.go"), "package x\n")

	step := unfinishedWorkCheck(dir)
	if step.Status != "ok" {
		t.Fatalf("status=%q output=%q", step.Status, step.Output)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

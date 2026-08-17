package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/core"
)

func writePipelineFile(t *testing.T, dir string) string {
	t.Helper()
	// A two-branch DAG so the run exercises the scheduler: resolve feeds
	// compile and test, both feed package.
	pipeline := `{
  "api_version": "platform-factory.dev/v1alpha1",
  "name": "example",
  "required_capabilities": ["cache", "parallel-stages"],
  "stages": [
    {"id": "resolve", "command": {"executable": "true"}},
    {"id": "compile", "depends_on": ["resolve"], "command": {"executable": "/bin/sh", "args": ["-c", "printf built > out.txt"]}, "outputs": [{"name": "bin", "path": "/out.txt"}]},
    {"id": "test", "depends_on": ["resolve"], "command": {"executable": "true"}},
    {"id": "package", "depends_on": ["compile", "test"], "command": {"executable": "true"}, "inputs": [{"stage": "compile", "name": "bin", "target": "/in/bin"}]}
  ]
}`
	name := filepath.Join(dir, "pipeline.json")
	if err := os.WriteFile(name, []byte(pipeline), 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestRunPipelinePlan(t *testing.T) {
	name := writePipelineFile(t, t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := runPipeline([]string{"plan", name}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{`"fingerprint"`, `"order"`, `"required_capabilities"`, "parallel-stages"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q: %s", want, stdout.String())
		}
	}
	stdout.Reset()
	if code := runPipeline([]string{"plan", "--format", "text", name}, &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), "level 0") {
		t.Fatalf("text plan code=%d stdout=%s", code, stdout.String())
	}
}

func TestRunPipelineRunExecutesAndWritesJournal(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh required")
	}
	work := t.TempDir()
	name := writePipelineFile(t, t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := runPipeline([]string{"run", "--sandbox", "off", "--workdir", work, name}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	journalPath := filepath.Join(work, "journal.json")
	data, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var journal struct {
		APIVersion  string `json:"api_version"`
		Fingerprint string `json:"pipeline_fingerprint"`
		Stages      []struct {
			ID    string `json:"id"`
			State string `json:"state"`
			Cache string `json:"cache"`
		} `json:"stages"`
	}
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatal(err)
	}
	if journal.APIVersion != "platform-factory.dev/journal/v1" || len(journal.Stages) != 4 {
		t.Fatalf("journal=%+v", journal)
	}
	for _, stage := range journal.Stages {
		if stage.State != "succeeded" {
			t.Fatalf("stage %s state=%s", stage.ID, stage.State)
		}
	}
	// The compile stage materialized its output into the package stage.
	if data, err := os.ReadFile(filepath.Join(work, "in", "bin")); err != nil || string(data) != "built" {
		t.Fatalf("materialized input=%q err=%v", data, err)
	}

	// A second run over the same cache is all hits and rewrites the same journal.
	stdout.Reset()
	if code := runPipeline([]string{"run", "--sandbox", "off", "--workdir", work, name}, &stdout, &stderr); code != 0 {
		t.Fatalf("second run code=%d stderr=%s", code, stderr.String())
	}
	second, _ := os.ReadFile(journalPath)
	if !strings.Contains(string(second), `"cache": "hit"`) {
		t.Fatalf("second run was not cached: %s", second)
	}
}

func TestRunPipelineRunEnforcesBudget(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh required")
	}
	dir := t.TempDir()
	slow := `{
  "api_version": "platform-factory.dev/v1alpha1",
  "name": "slow",
  "stages": [
    {"id": "sleeper", "command": {"executable": "/bin/sh", "args": ["-c", "sleep 5"]}}
  ]
}`
	name := filepath.Join(dir, "pipeline.json")
	if err := os.WriteFile(name, []byte(slow), 0o644); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := runPipeline([]string{
		"run", "--sandbox", "off", "--workdir", work, "--budget", "50ms", name,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "exceeded its configured budget") {
		t.Fatalf("stderr=%s", stderr.String())
	}
	journal, err := os.ReadFile(filepath.Join(work, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(journal), `"state": "budget_exceeded"`) {
		t.Fatalf("journal=%s", journal)
	}
}

func TestRunPipelineHelp(t *testing.T) {
	// The top-level dispatcher prints its own usage to stdout.
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}} {
		var stdout, stderr bytes.Buffer
		if code := runPipeline(args, &stdout, &stderr); code != 0 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "usage") {
			t.Fatalf("args=%v stdout=%s", args, stdout.String())
		}
	}
	// Subcommand -h is handled by the flag package itself, which prints
	// to stderr and returns flag.ErrHelp; only the exit code matters here.
	for _, args := range [][]string{{"plan", "-h"}, {"run", "-h"}} {
		var stdout, stderr bytes.Buffer
		if code := runPipeline(args, &stdout, &stderr); code != 0 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
}

func TestRunPipelinePlanRejectsMissingFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runPipeline([]string{"plan", filepath.Join(t.TempDir(), "missing.json")}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestMaterializePlainMountsCopiesDeclaredSources(t *testing.T) {
	root := t.TempDir()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	definition := core.Pipeline{
		Inputs: []core.Input{{ID: "src", Source: source}},
		Stages: []core.Stage{{
			ID:     "compile",
			Mounts: []core.Mount{{Source: "src", Target: "/in"}},
		}},
	}
	if err := materializePlainMounts(root, definition); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	copied, err := os.ReadFile(filepath.Join(root, "in", "file.txt"))
	if err != nil {
		t.Fatalf("expected the source to be copied into root/in: %v", err)
	}
	if string(copied) != "content" {
		t.Fatalf("copied content = %q", copied)
	}
}

func TestMaterializePlainMountsAllowsRepeatedIdenticalMount(t *testing.T) {
	root := t.TempDir()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	definition := core.Pipeline{
		Inputs: []core.Input{{ID: "src", Source: source}},
		Stages: []core.Stage{
			{ID: "a", Mounts: []core.Mount{{Source: "src", Target: "/in"}}},
			{ID: "b", Mounts: []core.Mount{{Source: "src", Target: "/in"}}},
		},
	}
	if err := materializePlainMounts(root, definition); err != nil {
		t.Fatalf("unexpected error for a repeated identical mount: %v", err)
	}
}

func TestMaterializePlainMountsRejectsConflictingTarget(t *testing.T) {
	root := t.TempDir()
	sourceA, sourceB := t.TempDir(), t.TempDir()
	definition := core.Pipeline{
		Inputs: []core.Input{{ID: "a", Source: sourceA}, {ID: "b", Source: sourceB}},
		Stages: []core.Stage{
			{ID: "x", Mounts: []core.Mount{{Source: "a", Target: "/in"}}},
			{ID: "y", Mounts: []core.Mount{{Source: "b", Target: "/in"}}},
		},
	}
	if err := materializePlainMounts(root, definition); err == nil {
		t.Fatal("expected an error when two different sources target the same mount point")
	}
}

func TestMaterializePlainMountsRejectsUndeclaredSource(t *testing.T) {
	root := t.TempDir()
	definition := core.Pipeline{
		Stages: []core.Stage{{ID: "x", Mounts: []core.Mount{{Source: "missing", Target: "/in"}}}},
	}
	if err := materializePlainMounts(root, definition); err == nil {
		t.Fatal("expected an error for a mount with no declared input")
	}
}

func TestMaterializePlainMountsRejectsMissingSourcePath(t *testing.T) {
	root := t.TempDir()
	definition := core.Pipeline{
		Inputs: []core.Input{{ID: "src", Source: filepath.Join(t.TempDir(), "does-not-exist")}},
		Stages: []core.Stage{{ID: "x", Mounts: []core.Mount{{Source: "src", Target: "/in"}}}},
	}
	if err := materializePlainMounts(root, definition); err == nil {
		t.Fatal("expected an error for a nonexistent source path")
	}
}

func TestMaterializePlainMountsRejectsNonDirectorySource(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	definition := core.Pipeline{
		Inputs: []core.Input{{ID: "src", Source: file}},
		Stages: []core.Stage{{ID: "x", Mounts: []core.Mount{{Source: "src", Target: "/in"}}}},
	}
	if err := materializePlainMounts(root, definition); err == nil {
		t.Fatal("expected an error when the source is a regular file, not a directory")
	}
}

func TestRunPipelineUsageErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"plan"},
		{"run", "--sandbox", "banana", "x.json"},
		{"run", "--budget", "-1s", "x.json"},
		{"plan", "--format", "yaml", "x.json"},
	} {
		if code := runPipeline(args, &stdout, &stderr); code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
}

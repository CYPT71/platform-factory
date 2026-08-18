package pipeline

import (
	"bytes"
	"testing"

	"os"
	"path/filepath"

	"github.com/CYPT71/platform-factory/internal/cache"
	apiv1alpha1 "github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/executor"
	"github.com/CYPT71/platform-factory/internal/pipeline"
)

func TestBuildStageRunnerSandboxRequiredFailsClosed(t *testing.T) {
	store, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	document := pipelineDocument{definition: apiv1alpha1.Pipeline{
		APIVersion: apiv1alpha1.APIVersion, Name: "x",
		Stages: []apiv1alpha1.Stage{{ID: "a", Command: apiv1alpha1.Command{Executable: "/bin/true"}}},
	}}
	support := executor.ProbeSandbox()
	if support.UserNamespaces {
		t.Skip("user namespaces available; the fail-closed path needs them absent")
	}
	var stderr bytes.Buffer
	if _, err := buildStageRunner("require", t.TempDir(), store, false, "", document, &stderr); err == nil {
		t.Fatal("sandbox require succeeded without namespace support")
	}
}

func TestBuildStageRunnerOffUsesPlainExecutor(t *testing.T) {
	store, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	document := pipelineDocument{definition: apiv1alpha1.Pipeline{APIVersion: apiv1alpha1.APIVersion, Name: "x"}}
	var stderr bytes.Buffer
	runner, err := buildStageRunner("off", t.TempDir(), store, true, "", document, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if runner.sandbox != "off" {
		t.Fatalf("sandbox=%s", runner.sandbox)
	}
}

func TestBuildJournalRecordsStates(t *testing.T) {
	store, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	document := pipelineDocument{fingerprint: "sha256:abc"}
	exec := executor.New(root, nil)
	caching := executor.NewCachingRunner(exec, root, cache.NewStoreAdapter(store), engineVersion, emptyBaseDigest(), "linux/amd64")
	runner := &stageRunner{executor: exec, caching: caching, sandbox: "off"}
	report := pipeline.ScheduleResult{Stages: []pipeline.StageResult{
		{Stage: "a", State: pipeline.StageSucceeded},
		{Stage: "b", State: pipeline.StageBlocked, Error: "dependency a did not succeed"},
	}}
	journal := buildJournal(document, report, runner)
	if journal["api_version"] != "platform-factory.dev/journal/v1" {
		t.Fatalf("journal=%+v", journal)
	}
	stages, ok := journal["stages"].([]map[string]any)
	if !ok || len(stages) != 2 {
		t.Fatalf("stages=%v", journal["stages"])
	}
	if stages[1]["error"] != "dependency a did not succeed" {
		t.Fatalf("stage=%+v", stages[1])
	}
}

func TestMaterializePlainMountsCopiesDeclaredSources(t *testing.T) {
	root := t.TempDir()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	definition := apiv1alpha1.Pipeline{
		Inputs: []apiv1alpha1.Input{{ID: "src", Source: source}},
		Stages: []apiv1alpha1.Stage{{
			ID:     "compile",
			Mounts: []apiv1alpha1.Mount{{Source: "src", Target: "/in"}},
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
	definition := apiv1alpha1.Pipeline{
		Inputs: []apiv1alpha1.Input{{ID: "src", Source: source}},
		Stages: []apiv1alpha1.Stage{
			{ID: "a", Mounts: []apiv1alpha1.Mount{{Source: "src", Target: "/in"}}},
			{ID: "b", Mounts: []apiv1alpha1.Mount{{Source: "src", Target: "/in"}}},
		},
	}
	if err := materializePlainMounts(root, definition); err != nil {
		t.Fatalf("unexpected error for a repeated identical mount: %v", err)
	}
}

func TestMaterializePlainMountsRejectsConflictingTarget(t *testing.T) {
	root := t.TempDir()
	sourceA, sourceB := t.TempDir(), t.TempDir()
	definition := apiv1alpha1.Pipeline{
		Inputs: []apiv1alpha1.Input{{ID: "a", Source: sourceA}, {ID: "b", Source: sourceB}},
		Stages: []apiv1alpha1.Stage{
			{ID: "x", Mounts: []apiv1alpha1.Mount{{Source: "a", Target: "/in"}}},
			{ID: "y", Mounts: []apiv1alpha1.Mount{{Source: "b", Target: "/in"}}},
		},
	}
	if err := materializePlainMounts(root, definition); err == nil {
		t.Fatal("expected an error when two different sources target the same mount point")
	}
}

func TestMaterializePlainMountsRejectsUndeclaredSource(t *testing.T) {
	root := t.TempDir()
	definition := apiv1alpha1.Pipeline{
		Stages: []apiv1alpha1.Stage{{ID: "x", Mounts: []apiv1alpha1.Mount{{Source: "missing", Target: "/in"}}}},
	}
	if err := materializePlainMounts(root, definition); err == nil {
		t.Fatal("expected an error for a mount with no declared input")
	}
}

func TestMaterializePlainMountsRejectsMissingSourcePath(t *testing.T) {
	root := t.TempDir()
	definition := apiv1alpha1.Pipeline{
		Inputs: []apiv1alpha1.Input{{ID: "src", Source: filepath.Join(t.TempDir(), "does-not-exist")}},
		Stages: []apiv1alpha1.Stage{{ID: "x", Mounts: []apiv1alpha1.Mount{{Source: "src", Target: "/in"}}}},
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
	definition := apiv1alpha1.Pipeline{
		Inputs: []apiv1alpha1.Input{{ID: "src", Source: file}},
		Stages: []apiv1alpha1.Stage{{ID: "x", Mounts: []apiv1alpha1.Mount{{Source: "src", Target: "/in"}}}},
	}
	if err := materializePlainMounts(root, definition); err == nil {
		t.Fatal("expected an error when the source is a regular file, not a directory")
	}
}

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/cache"
	apiv1alpha1 "github.com/CYPT71/secure-oci-base/internal/core"
	"github.com/CYPT71/secure-oci-base/internal/executor"
	"github.com/CYPT71/secure-oci-base/internal/pipeline"
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

func TestRunPipelineRunRejectsInvalidPipelineFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runPipeline([]string{"run", "--sandbox", "off", "/does/not/exist.json"}, &stdout, &stderr); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stderr.String(), "pipeline run") {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

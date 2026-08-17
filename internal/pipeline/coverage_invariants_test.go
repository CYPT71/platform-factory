package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	api "github.com/CYPT71/platform-factory/internal/core"
)

func TestCanonicalJSONNormalizesEveryUnorderedCollection(t *testing.T) {
	first := validPipeline()
	first.RequiredCapabilities = []string{"secrets", "artifacts", "cache"}
	first.Inputs = append(first.Inputs, api.Input{ID: "another", Kind: "directory", Source: "vendor", Digest: testDigest})
	first.Outputs = append(first.Outputs, api.Output{Name: "archive", Stage: "package", Artifact: "image"})
	stage := &first.Stages[0]
	stage.Mounts = append(stage.Mounts, api.Mount{Source: "another", Target: "/a", ReadOnly: true})
	stage.Secrets = append(stage.Secrets, api.SecretReference{ID: "alpha", Target: "/run/secrets/a"})
	stage.Caches = append(stage.Caches, api.CacheMount{ID: "alpha", Target: "/a-cache"})
	stage.Inputs = append(stage.Inputs, api.ArtifactReference{Stage: "assets", Name: "static"})
	stage.Outputs = append(stage.Outputs, api.ArtifactDeclaration{Name: "archive", Path: "/out/archive"})
	stage.Command.Args = []string{"z", "a"} // Command order is semantic and must remain unchanged.

	second := first
	second.RequiredCapabilities = reversed(first.RequiredCapabilities)
	second.Inputs = reversed(first.Inputs)
	second.Outputs = reversed(first.Outputs)
	second.Stages = reversed(first.Stages)
	for i := range second.Stages {
		second.Stages[i].DependsOn = reversed(second.Stages[i].DependsOn)
		second.Stages[i].Mounts = reversed(second.Stages[i].Mounts)
		second.Stages[i].Secrets = reversed(second.Stages[i].Secrets)
		second.Stages[i].Caches = reversed(second.Stages[i].Caches)
		second.Stages[i].Inputs = reversed(second.Stages[i].Inputs)
		second.Stages[i].Outputs = reversed(second.Stages[i].Outputs)
	}

	want, err := CanonicalJSON(first)
	if err != nil {
		t.Fatal(err)
	}
	got, err := CanonicalJSON(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("canonical form depends on collection order\nfirst=%s\nsecond=%s", want, got)
	}
	if !reflect.DeepEqual(first.Stages[0].Command.Args, []string{"z", "a"}) || first.Stages[2].Env["LANG"] != "C" {
		t.Fatal("canonicalization mutated semantic command or environment input")
	}
}

func TestCanonicalJSONNormalizesDefaultsAndEmptyCapabilities(t *testing.T) {
	definition := validPipeline()
	definition.RequiredCapabilities = []string{}
	definition.Stages[1].Network = ""
	data, err := CanonicalJSON(definition)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"network":"none"`)) || bytes.Contains(data, []byte(`"requiredCapabilities":[]`)) {
		t.Fatalf("defaults were not canonicalized: %s", data)
	}
}

func TestCheckpointStoresRejectInvalidInputsAndMissingExports(t *testing.T) {
	memory := NewMemoryCheckpointStore()
	if err := memory.Save(nil); err == nil {
		t.Fatal("memory store accepted nil checkpoint")
	}
	if _, err := memory.Import(bytes.NewBufferString("{")); err == nil {
		t.Fatal("memory store accepted corrupt checkpoint")
	}
	if err := memory.Export(&bytes.Buffer{}, "missing"); err == nil {
		t.Fatal("memory store exported missing checkpoint")
	}

	store, err := NewCheckpointStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(nil); err == nil {
		t.Fatal("file store accepted nil checkpoint")
	}
	if err := store.Save(&Checkpoint{}); err == nil {
		t.Fatal("file store accepted empty checkpoint ID")
	}
	if _, err := store.Import(bytes.NewBufferString("not-json")); err == nil {
		t.Fatal("file store accepted corrupt checkpoint")
	}
	if err := store.Export(&bytes.Buffer{}, "missing"); err == nil {
		t.Fatal("file store exported missing checkpoint")
	}
}

func TestCheckpointStoreRecoveryIgnoresUnrelatedAndCorruptEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewCheckpointStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.ListIncomplete(); len(got) != 0 {
		t.Fatalf("recovered invalid checkpoints: %+v", got)
	}
}

func TestCheckpointManagerUpdateResumeAndDeleteBranches(t *testing.T) {
	store := NewMemoryCheckpointStore()
	manager := NewCheckpointManager(store, "pipeline")
	createdFailure := manager.Update("failed", StageFailed, "", errors.New("boom"))
	if createdFailure.State != StageFailed || createdFailure.Error != "boom" {
		t.Fatalf("failure checkpoint=%+v", createdFailure)
	}
	createdSuccess := manager.Update("done", StageSucceeded, "digest", nil)
	if createdSuccess.State != StageSucceeded || createdSuccess.Outputs != "" {
		t.Fatalf("new checkpoint state=%+v", createdSuccess)
	}
	if manager.CanResume("missing") || manager.CanResume("done") {
		t.Fatal("terminal or missing checkpoint reported resumable")
	}
	budget := CreateCheckpoint("pipeline", "budget", StageBudgetExceeded)
	manager.checkpoints["budget"] = budget
	if !manager.CanResume("budget") {
		t.Fatal("budget-exceeded checkpoint should be resumable")
	}
	if err := manager.Delete(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointableRunnerFailureAndNonRetryableBranches(t *testing.T) {
	manager := NewCheckpointManager(NewMemoryCheckpointStore(), "pipeline")
	runner := &CheckpointableRunner{
		Manager: manager,
		Runner:  StageRunnerFunc(func(context.Context, api.Stage) error { return errors.New("execution failed") }),
	}
	if err := runner.Run(context.Background(), api.Stage{ID: "fails"}); err == nil {
		t.Fatal("runner failure was hidden")
	}
	cp := CreateCheckpoint("pipeline", "blocked", StageFailed)
	cp.Retryable = false
	cp.Error = "permanent"
	manager.checkpoints["blocked"] = cp
	if err := runner.Run(context.Background(), api.Stage{ID: "blocked"}); err == nil {
		t.Fatal("non-retryable checkpoint was executed")
	}
}

func TestValidateRequiredCapabilitiesCompatibilityHelper(t *testing.T) {
	if issues := validateRequiredCapabilities([]string{CapabilityCache, CapabilitySandbox}); len(issues) != 0 {
		t.Fatalf("valid capabilities rejected: %v", issues)
	}
	if issues := validateRequiredCapabilities([]string{"unknown", CapabilityCache, CapabilityCache}); len(issues) != 2 {
		t.Fatalf("expected unknown and duplicate issues, got %v", issues)
	}
}

func reversed[T any](values []T) []T {
	result := append([]T(nil), values...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

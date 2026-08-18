package pipeline

// These tests exercise the Service entry points (New, Decode, Plan, Run)
// end to end: a real pipeline JSON file on disk, a real (unsandboxed)
// executor, and a real on-disk cache, the same way cmd/platform-factory
// drives this package. buildStageRunner, materializePlainMounts and
// buildJournal already have focused unit tests above; these instead
// cover the setup/decode/schedule/journal wiring that only Decode, Plan
// and Run themselves perform.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apiv1alpha1 "github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/executor"
	"github.com/CYPT71/platform-factory/internal/pipeline"
)

// writePipelineFile marshals definition as the pipeline JSON file Decode,
// Plan and Run all read from disk.
func writePipelineFile(t *testing.T, definition apiv1alpha1.Pipeline) string {
	t.Helper()
	data, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pipeline.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// shPipeline returns a minimal, decodable, two-stage pipeline whose
// stages just run a shell command - fast and portable, so Run can
// actually execute it through the plain (sandbox "off") executor.
func shPipeline(name string, stages ...apiv1alpha1.Stage) apiv1alpha1.Pipeline {
	return apiv1alpha1.Pipeline{
		APIVersion: apiv1alpha1.APIVersion,
		Name:       name,
		Stages:     stages,
	}
}

func shStage(id string, script string, dependsOn ...string) apiv1alpha1.Stage {
	return apiv1alpha1.Stage{
		ID:        id,
		DependsOn: dependsOn,
		Command:   apiv1alpha1.Command{Executable: "sh", Args: []string{"-c", script}},
	}
}

func TestServiceDecodeReturnsValidatedDefinition(t *testing.T) {
	path := writePipelineFile(t, shPipeline("demo", shStage("a", "exit 0"), shStage("b", "exit 0", "a")))
	definition, err := New().Decode(path)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Name != "demo" || len(definition.Stages) != 2 {
		t.Fatalf("definition=%+v", definition)
	}
}

func TestServiceDecodeMissingFileReturnsNotExistError(t *testing.T) {
	_, err := New().Decode(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Fatal("expected an error for a nonexistent pipeline file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err=%v, want a not-exist error", err)
	}
}

func TestServiceDecodeRejectsInvalidDefinition(t *testing.T) {
	invalid := shPipeline("demo", shStage("a", "exit 0", "missing"))
	path := writePipelineFile(t, invalid)
	if _, err := New().Decode(path); err == nil || !strings.Contains(err.Error(), "unknown stage") {
		t.Fatalf("err=%v, want a validation error about the unknown dependency", err)
	}
}

func TestServicePlanReturnsOrderLevelsFingerprintAndCapabilities(t *testing.T) {
	definition := shPipeline("demo", shStage("a", "exit 0"), shStage("b", "exit 0", "a"))
	definition.RequiredCapabilities = []string{pipeline.CapabilityCache}
	path := writePipelineFile(t, definition)

	result, err := New().Plan(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "demo" || result.PipelineAPIVersion != apiv1alpha1.APIVersion {
		t.Fatalf("result=%+v", result)
	}
	if !strings.HasPrefix(result.Fingerprint, "sha256:") {
		t.Fatalf("fingerprint=%q", result.Fingerprint)
	}
	if len(result.Order) != 2 || result.Order[0] != "a" || result.Order[1] != "b" {
		t.Fatalf("order=%v", result.Order)
	}
	if len(result.Levels) != 2 {
		t.Fatalf("levels=%v", result.Levels)
	}
	if len(result.RequiredCapabilities) != 1 || result.RequiredCapabilities[0] != pipeline.CapabilityCache {
		t.Fatalf("required capabilities=%v", result.RequiredCapabilities)
	}
	available := pipeline.KnownCapabilities()
	if len(result.AvailableCapabilities) != len(available) {
		t.Fatalf("available capabilities=%v, want %v", result.AvailableCapabilities, available)
	}
	for index, name := range available {
		if result.AvailableCapabilities[index] != name {
			t.Fatalf("available capabilities=%v, want %v", result.AvailableCapabilities, available)
		}
	}
}

func TestServicePlanPropagatesDecodeErrorAndReturnsZeroResult(t *testing.T) {
	result, err := New().Plan(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Fatal("expected an error for a nonexistent pipeline file")
	}
	if result.Name != "" || result.Fingerprint != "" || result.Order != nil || result.Levels != nil {
		t.Fatalf("result=%+v, want the zero value on a decode failure", result)
	}
}

func TestServiceRunSucceedsAndJournalsCacheMissAndOutput(t *testing.T) {
	definition := shPipeline("run-ok", shStage("a", "printf hi"))
	path := writePipelineFile(t, definition)
	workdir := t.TempDir()

	var stderr bytes.Buffer
	result, err := New().Run(context.Background(), RunOptions{
		Path: path, Workdir: workdir, CacheDir: filepath.Join(workdir, "cache-store"),
		Parallelism: 1, SandboxMode: "off",
	}, &stderr)
	if err != nil {
		t.Fatalf("err=%v stderr=%s", err, stderr.String())
	}
	if result.Name != "run-ok" {
		t.Fatalf("name=%q", result.Name)
	}
	if result.JournalPath != filepath.Join(workdir, "journal.json") {
		t.Fatalf("journal path=%q", result.JournalPath)
	}
	if len(result.Stages) != 1 || result.Stages[0].State != pipeline.StageSucceeded {
		t.Fatalf("stages=%+v", result.Stages)
	}

	data, err := os.ReadFile(result.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk["sandbox"] != "off" {
		t.Fatalf("on-disk journal=%+v", onDisk)
	}

	stages, ok := result.Journal["stages"].([]map[string]any)
	if !ok || len(stages) != 1 {
		t.Fatalf("journal stages=%v", result.Journal["stages"])
	}
	entry := stages[0]
	if entry["cache"] != "miss" {
		t.Fatalf("entry=%+v, want a cache miss on a first run", entry)
	}
	if entry["exit_code"] != 0 {
		t.Fatalf("entry=%+v", entry)
	}
	if entry["stdout"] != "hi" {
		t.Fatalf("entry stdout=%q, want the stage's captured output", entry["stdout"])
	}
}

func TestServiceRunDefaultsWorkdirToATempDirWhenNotProvided(t *testing.T) {
	path := writePipelineFile(t, shPipeline("run-default-workdir", shStage("a", "exit 0")))
	result, err := New().Run(context.Background(), RunOptions{Path: path, SandboxMode: "off", Parallelism: 1}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(result.JournalPath))
	if !strings.Contains(filepath.Dir(result.JournalPath), "platform-factory-pipeline-") {
		t.Fatalf("journal path=%q, want it under a generated platform-factory-pipeline-* temp dir", result.JournalPath)
	}
	if _, err := os.Stat(result.JournalPath); err != nil {
		t.Fatalf("journal was not written: %v", err)
	}
}

func TestServiceRunReportsCacheHitOnASecondRunWithTheSameCache(t *testing.T) {
	definition := shPipeline("run-cache", shStage("a", "exit 0"))
	path := writePipelineFile(t, definition)
	cacheDir := t.TempDir()

	first, err := New().Run(context.Background(), RunOptions{
		Path: path, Workdir: t.TempDir(), CacheDir: cacheDir, Parallelism: 1, SandboxMode: "off",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	firstStages := first.Journal["stages"].([]map[string]any)
	if firstStages[0]["cache"] != "miss" {
		t.Fatalf("first run entry=%+v, want a cache miss", firstStages[0])
	}

	second, err := New().Run(context.Background(), RunOptions{
		Path: path, Workdir: t.TempDir(), CacheDir: cacheDir, Parallelism: 1, SandboxMode: "off",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	secondStages := second.Journal["stages"].([]map[string]any)
	if secondStages[0]["cache"] != "hit" {
		t.Fatalf("second run entry=%+v, want a cache hit reusing the first run's record", secondStages[0])
	}
	// A replayed cache hit never executes the command, so no exec.Result
	// exists for it and the journal must not report an exit code.
	if _, ok := secondStages[0]["exit_code"]; ok {
		t.Fatalf("second run entry=%+v, want no exit_code for a cache hit", secondStages[0])
	}
}

func TestServiceRunStageFailureStillWritesJournalAndReturnsScheduleError(t *testing.T) {
	definition := shPipeline("run-fail", shStage("a", "exit 5"))
	path := writePipelineFile(t, definition)

	result, err := New().Run(context.Background(), RunOptions{
		Path: path, Workdir: t.TempDir(), Parallelism: 1, SandboxMode: "off",
	}, io.Discard)
	if err == nil {
		t.Fatal("expected the failing stage to surface as a run error")
	}
	var scheduleErr *pipeline.ScheduleError
	if !errors.As(err, &scheduleErr) {
		t.Fatalf("err=%v (%T), want a *pipeline.ScheduleError", err, err)
	}
	if result.JournalPath == "" {
		t.Fatal("expected a populated result with a written journal despite the stage failure")
	}
	if len(result.Stages) != 1 || result.Stages[0].State != pipeline.StageFailed {
		t.Fatalf("stages=%+v", result.Stages)
	}
	stages := result.Journal["stages"].([]map[string]any)
	if stages[0]["exit_code"] != 5 {
		t.Fatalf("entry=%+v, want exit_code 5", stages[0])
	}
}

func TestServiceRunBudgetExceededStillWritesJournalAndReturnsBudgetError(t *testing.T) {
	definition := shPipeline("run-budget", shStage("a", "sleep 2"))
	path := writePipelineFile(t, definition)

	result, err := New().Run(context.Background(), RunOptions{
		Path: path, Workdir: t.TempDir(), Parallelism: 1, SandboxMode: "off",
		Budget: 20 * time.Millisecond,
	}, io.Discard)
	if err == nil {
		t.Fatal("expected the budget to be exceeded")
	}
	var budgetErr *pipeline.ScheduleBudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("err=%v (%T), want a *pipeline.ScheduleBudgetExceededError", err, err)
	}
	if result.JournalPath == "" {
		t.Fatal("expected a populated result with a written journal despite the exceeded budget")
	}
	if len(result.Stages) != 1 || result.Stages[0].State != pipeline.StageBudgetExceeded {
		t.Fatalf("stages=%+v, want the in-flight stage reported as budget_exceeded", result.Stages)
	}
}

func TestServiceRunCacheOpenErrorReturnsZeroResult(t *testing.T) {
	path := writePipelineFile(t, shPipeline("run-bad-cache", shStage("a", "exit 0")))
	result, err := New().Run(context.Background(), RunOptions{
		Path: path, Workdir: t.TempDir(), CacheDir: "bad\x00dir", SandboxMode: "off",
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "open cache") {
		t.Fatalf("err=%v, want an 'open cache' error for a NUL-containing cache dir", err)
	}
	if result.JournalPath != "" || result.Journal != nil || result.Stages != nil {
		t.Fatalf("result=%+v, want the zero value on a cache-open failure", result)
	}
}

func TestServiceRunDecodeErrorReturnsZeroResult(t *testing.T) {
	result, err := New().Run(context.Background(), RunOptions{
		Path: filepath.Join(t.TempDir(), "missing.json"),
	}, io.Discard)
	if err == nil {
		t.Fatal("expected an error for a nonexistent pipeline file")
	}
	if result.JournalPath != "" || result.Journal != nil || result.Stages != nil || result.Name != "" {
		t.Fatalf("result=%+v, want the zero value on a decode failure", result)
	}
}

func TestServiceRunSandboxRequireFailsClosedBeforeWritingAJournal(t *testing.T) {
	if executor.ProbeSandbox().UserNamespaces {
		t.Skip("user namespaces available; the fail-closed path needs them absent")
	}
	path := writePipelineFile(t, shPipeline("run-require", shStage("a", "exit 0")))
	result, err := New().Run(context.Background(), RunOptions{
		Path: path, Workdir: t.TempDir(), SandboxMode: "require",
	}, io.Discard)
	if err == nil {
		t.Fatal("expected sandbox require to fail closed without namespace support")
	}
	if result.JournalPath != "" {
		t.Fatalf("result=%+v, want no journal written for a setup failure", result)
	}
}

func TestServiceRunUsesSecretDirResolverWhenSecretEnvIsOff(t *testing.T) {
	// The plain executor refuses any stage that actually declares
	// secrets, so this only exercises buildStageRunner's secretDir
	// branch (WithSecretResolver(DirResolver{...})) end to end through
	// Run - not secret delivery itself, which is sandboxed-only.
	path := writePipelineFile(t, shPipeline("run-secret-dir", shStage("a", "exit 0")))
	result, err := New().Run(context.Background(), RunOptions{
		Path: path, Workdir: t.TempDir(), Parallelism: 1, SandboxMode: "off",
		SecretDir: t.TempDir(),
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stages) != 1 || result.Stages[0].State != pipeline.StageSucceeded {
		t.Fatalf("stages=%+v", result.Stages)
	}
}

func TestServiceRunJournalsStageStderr(t *testing.T) {
	path := writePipelineFile(t, shPipeline("run-stderr", shStage("a", "printf oops 1>&2")))
	result, err := New().Run(context.Background(), RunOptions{
		Path: path, Workdir: t.TempDir(), Parallelism: 1, SandboxMode: "off",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	stages := result.Journal["stages"].([]map[string]any)
	if stages[0]["stderr"] != "oops" {
		t.Fatalf("entry=%+v, want the captured stderr", stages[0])
	}
}

func TestServiceRunMountWithMissingSourcePathReturnsErrorBeforeScheduling(t *testing.T) {
	definition := shPipeline("run-bad-mount", apiv1alpha1.Stage{
		ID:      "a",
		Command: apiv1alpha1.Command{Executable: "sh", Args: []string{"-c", "exit 0"}},
		Mounts:  []apiv1alpha1.Mount{{Source: "src", Target: "/in"}},
	})
	definition.Inputs = []apiv1alpha1.Input{{
		ID: "src", Kind: "directory", Source: filepath.Join(t.TempDir(), "does-not-exist"),
		Digest: "sha256:" + strings.Repeat("a", 64),
	}}
	path := writePipelineFile(t, definition)

	result, err := New().Run(context.Background(), RunOptions{
		Path: path, Workdir: t.TempDir(), Parallelism: 1, SandboxMode: "off",
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "pipeline input") {
		t.Fatalf("err=%v, want a pipeline-input error from materializing the mount", err)
	}
	if result.JournalPath != "" {
		t.Fatalf("result=%+v, want the zero value: this fails before scheduling", result)
	}
}

func TestServiceRunMkdirAllWorkdirErrorReturnsZeroResult(t *testing.T) {
	path := writePipelineFile(t, shPipeline("run-bad-workdir", shStage("a", "exit 0")))
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := New().Run(context.Background(), RunOptions{
		Path: path, Workdir: filepath.Join(blocker, "workdir"), Parallelism: 1, SandboxMode: "off",
	}, io.Discard)
	if err == nil {
		t.Fatal("expected MkdirAll to fail because a path component is a regular file")
	}
	if result.JournalPath != "" {
		t.Fatalf("result=%+v, want the zero value on a workdir setup failure", result)
	}
}

func TestServiceRunWriteJournalErrorReturnsZeroResult(t *testing.T) {
	path := writePipelineFile(t, shPipeline("run-bad-journal", shStage("a", "exit 0")))
	workdir := t.TempDir()
	// Pre-create journal.json as a directory so the final os.WriteFile
	// of the journal fails.
	if err := os.MkdirAll(filepath.Join(workdir, "journal.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := New().Run(context.Background(), RunOptions{
		Path: path, Workdir: workdir, Parallelism: 1, SandboxMode: "off",
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "write journal") {
		t.Fatalf("err=%v, want a write-journal error", err)
	}
	if result.JournalPath != "" || result.Journal != nil || result.Stages != nil {
		t.Fatalf("result=%+v, want the zero value on a journal write failure", result)
	}
}

func TestServiceRunSandboxAutoFallsBackAndReportsUnavailability(t *testing.T) {
	if executor.ProbeSandbox().UserNamespaces {
		t.Skip("user namespaces available; the fallback path needs them absent")
	}
	path := writePipelineFile(t, shPipeline("run-auto", shStage("a", "exit 0")))
	var stderr bytes.Buffer
	result, err := New().Run(context.Background(), RunOptions{
		Path: path, Workdir: t.TempDir(), SandboxMode: "auto", Parallelism: 1,
	}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if result.Journal["sandbox"] != "off" {
		t.Fatalf("journal=%+v, want the auto fallback to run unsandboxed", result.Journal)
	}
	if !strings.Contains(stderr.String(), "sandbox unavailable") {
		t.Fatalf("stderr=%q, want a sandbox-unavailable warning", stderr.String())
	}
}

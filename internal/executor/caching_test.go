package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CYPT71/platform-factory/internal/core"
)

const cachingTestDigest = "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

// writingRunner counts invocations and writes content to every declared
// output path (mapped under root) each time it runs.
type writingRunner struct {
	root    string
	content string
	calls   atomic.Int64
}

func (r *writingRunner) Run(_ context.Context, stage core.Stage) error {
	r.calls.Add(1)
	for _, output := range stage.Outputs {
		path := MapPath(r.root, output.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(r.content), 0644); err != nil {
			return err
		}
	}
	return nil
}

func newTestStoreAdapter(t *testing.T) core.CacheStore {
	t.Helper()
	return newTestCacheStore()
}

func buildStage() core.Stage {
	return core.Stage{
		ID:      "build",
		Command: core.Command{Executable: "true"},
		Outputs: []core.ArtifactDeclaration{{Name: "binary", Path: "/out/binary"}},
	}
}

func TestCachingRunnerMissesThenHits(t *testing.T) {
	root := t.TempDir()
	inner := &writingRunner{root: root, content: "v1"}
	runner := NewCachingRunner(inner, root, newTestStoreAdapter(t), "engine/v0", cachingTestDigest, "linux/amd64")

	stage := buildStage()
	if err := runner.Run(context.Background(), stage); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if inner.calls.Load() != 1 {
		t.Fatalf("calls=%d", inner.calls.Load())
	}
	if len(runner.Hits()) != 0 {
		t.Fatalf("hits=%v", runner.Hits())
	}

	if err := runner.Run(context.Background(), stage); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if inner.calls.Load() != 1 {
		t.Fatalf("inner ran again: calls=%d", inner.calls.Load())
	}
	if hits := runner.Hits(); len(hits) != 1 || hits[0] != "build" {
		t.Fatalf("hits=%v", hits)
	}
}

func TestCachingRunnerNeverPersistsSecretTaintedStage(t *testing.T) {
	root := t.TempDir()
	inner := &writingRunner{root: root, content: "contains-derived-secret"}
	runner := NewCachingRunner(inner, root, newTestStoreAdapter(t), "engine/v0", cachingTestDigest, "linux/amd64")
	stage := buildStage()
	stage.Secrets = []core.SecretReference{{ID: "token", Target: "/run/secrets/token"}}
	if err := runner.Run(context.Background(), stage); err != nil {
		t.Fatal(err)
	}
	if _, ok := runner.Output("build", "binary"); ok {
		t.Fatal("secret-tainted output entered the CAS")
	}
	if err := runner.Run(context.Background(), stage); err != nil {
		t.Fatal(err)
	}
	if inner.calls.Load() != 2 || len(runner.Hits()) != 0 {
		t.Fatalf("secret stage was cached: calls=%d hits=%v", inner.calls.Load(), runner.Hits())
	}
}

func TestCachingRunnerOutputReflectsFreshAndCachedRuns(t *testing.T) {
	root := t.TempDir()
	inner := &writingRunner{root: root, content: "v1"}
	runner := NewCachingRunner(inner, root, newTestStoreAdapter(t), "engine/v0", cachingTestDigest, "linux/amd64")

	if _, ok := runner.Output("build", "binary"); ok {
		t.Fatal("expected no output before the stage has run")
	}

	stage := buildStage()
	if err := runner.Run(context.Background(), stage); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first, ok := runner.Output("build", "binary")
	if !ok {
		t.Fatal("expected an output after a fresh run")
	}

	if err := runner.Run(context.Background(), stage); err != nil {
		t.Fatalf("second run: %v", err)
	}
	second, ok := runner.Output("build", "binary")
	if !ok || second != first {
		t.Fatalf("expected the cache-hit replay to report the same descriptor: first=%+v second=%+v ok=%v", first, second, ok)
	}

	if _, ok := runner.Output("build", "does-not-exist"); ok {
		t.Fatal("expected no output for an undeclared artifact name")
	}
	if _, ok := runner.Output("unknown-stage", "binary"); ok {
		t.Fatal("expected no output for an unknown stage")
	}
}

func TestCachingRunnerRerunsWhenStageChanges(t *testing.T) {
	root := t.TempDir()
	inner := &writingRunner{root: root, content: "v1"}
	runner := NewCachingRunner(inner, root, newTestStoreAdapter(t), "engine/v0", cachingTestDigest, "linux/amd64")

	stage := buildStage()
	if err := runner.Run(context.Background(), stage); err != nil {
		t.Fatalf("run: %v", err)
	}

	stage.Command.Args = []string{"--changed"}
	if err := runner.Run(context.Background(), stage); err != nil {
		t.Fatalf("run after change: %v", err)
	}
	if inner.calls.Load() != 2 {
		t.Fatalf("calls=%d, want 2 after stage definition changed", inner.calls.Load())
	}
}

func TestCachingRunnerPropagatesInnerError(t *testing.T) {
	root := t.TempDir()
	inner := StageRunnerFunc(func(context.Context, core.Stage) error {
		return context.DeadlineExceeded
	})
	runner := NewCachingRunner(inner, root, newTestStoreAdapter(t), "engine/v0", cachingTestDigest, "linux/amd64")

	if err := runner.Run(context.Background(), buildStage()); err == nil {
		t.Fatal("expected inner error to propagate")
	}
}

func TestCachingRunnerFailsWhenDeclaredOutputMissing(t *testing.T) {
	root := t.TempDir()
	inner := StageRunnerFunc(func(context.Context, core.Stage) error { return nil })
	runner := NewCachingRunner(inner, root, newTestStoreAdapter(t), "engine/v0", cachingTestDigest, "linux/amd64")

	if err := runner.Run(context.Background(), buildStage()); err == nil {
		t.Fatal("expected an error when the declared output file does not exist")
	}
}

func TestCachingRunnerChainsInputDigestsAcrossStages(t *testing.T) {
	root := t.TempDir()

	produce := core.Stage{
		ID:      "produce",
		Command: core.Command{Executable: "true"},
		Outputs: []core.ArtifactDeclaration{{Name: "artifact", Path: "/out/artifact"}},
	}
	consume := core.Stage{
		ID:        "consume",
		DependsOn: []string{"produce"},
		Command:   core.Command{Executable: "true"},
		Inputs:    []core.ArtifactReference{{Stage: "produce", Name: "artifact"}},
	}

	// First pipeline run: produce writes "v1".
	producer := &writingRunner{root: root, content: "v1"}
	consumeCalls := &atomic.Int64{}
	consumer := StageRunnerFunc(func(context.Context, core.Stage) error {
		consumeCalls.Add(1)
		return nil
	})
	runner := NewCachingRunner(dispatchRunner{"produce": producer, "consume": consumer}, root, newTestStoreAdapter(t), "engine/v0", cachingTestDigest, "linux/amd64")

	if err := runner.Run(context.Background(), produce); err != nil {
		t.Fatalf("produce: %v", err)
	}
	if err := runner.Run(context.Background(), consume); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if consumeCalls.Load() != 1 {
		t.Fatalf("consumeCalls=%d", consumeCalls.Load())
	}

	// Second pipeline run, same cache. produce's own definition changes
	// (forcing a legitimate re-run that writes different content) while
	// consume's Stage definition stays byte-identical to the first run:
	// consume must still re-run, because its *resolved input digest*
	// changed even though nothing in consume's own declaration did.
	root2 := t.TempDir()
	producer2 := &writingRunner{root: root2, content: "v2"}
	produceV2 := produce
	produceV2.Command.Args = []string{"--changed"}
	runner2 := NewCachingRunner(dispatchRunner{"produce": producer2, "consume": consumer}, root2, newTestStoreAdapter(t), "engine/v0", cachingTestDigest, "linux/amd64")
	if err := runner2.Run(context.Background(), produceV2); err != nil {
		t.Fatalf("produce v2: %v", err)
	}
	if producer2.calls.Load() != 1 {
		t.Fatalf("producer2.calls=%d, want produce to have legitimately re-run", producer2.calls.Load())
	}
	if err := runner2.Run(context.Background(), consume); err != nil {
		t.Fatalf("consume v2: %v", err)
	}
	if consumeCalls.Load() != 2 {
		t.Fatalf("consumeCalls=%d, want 2 after producer content changed", consumeCalls.Load())
	}
}

func TestCachingRunnerRejectsInputsWithoutPublishedProducerOutput(t *testing.T) {
	root := t.TempDir()
	runner := NewCachingRunner(&writingRunner{root: root}, root, newTestCacheStore(), "v1", "sha256:base", "linux/amd64")
	tests := []struct {
		name  string
		setup func()
		input core.ArtifactReference
		want  string
	}{
		{
			name:  "producer not run",
			setup: func() {},
			input: core.ArtifactReference{Stage: "build", Name: "binary"},
			want:  "has not produced outputs yet",
		},
		{
			name: "artifact not published",
			setup: func() {
				runner.setOutputs("build", map[string]core.Descriptor{"other": {Digest: "sha256:other"}})
			},
			input: core.ArtifactReference{Stage: "build", Name: "binary"},
			want:  "did not produce artifact \"binary\"",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()
			err := runner.Run(context.Background(), core.Stage{
				ID: "package", Command: core.Command{Executable: "true"}, Inputs: []core.ArtifactReference{tc.input},
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestCachingRunnerTreatsEvictedBlobAsMiss(t *testing.T) {
	root := t.TempDir()
	inner := &writingRunner{root: root, content: "v1"}
	store := newTestCacheStore()
	runner := NewCachingRunner(inner, root, store, "engine/v0", cachingTestDigest, "linux/amd64")

	stage := buildStage()
	if err := runner.Run(context.Background(), stage); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// The record itself survives, but nothing ever leased the blob it
	// points at, so a GC sweep (e.g. an unrelated build reclaiming space)
	// can legitimately remove it. Confirm the next run detects the missing
	// blob via Verify and falls back to re-executing instead of erroring
	// out or claiming success without real content.
	descriptor, ok := runner.Output("build", "binary")
	if !ok {
		t.Fatal("missing cached output")
	}
	store.evict(descriptor.Digest)

	if err := runner.Run(context.Background(), stage); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if inner.calls.Load() != 2 {
		t.Fatalf("calls=%d, want 2 after the cached blob was evicted", inner.calls.Load())
	}
}

// dispatchRunner routes Run calls to the sub-runner registered for the
// stage's ID, so a single CachingRunner can wrap distinct behavior per
// stage in a test without a full scheduler.
type dispatchRunner map[string]StageRunner

func (d dispatchRunner) Run(ctx context.Context, stage core.Stage) error {
	return d[stage.ID].Run(ctx, stage)
}

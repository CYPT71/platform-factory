package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/core"
)

type recordingRunner struct {
	root  string
	stage core.Stage
}

func (r *recordingRunner) Run(_ context.Context, stage core.Stage) error {
	r.stage = stage
	return nil
}

// staticSource is an artifactSource that returns pre-defined descriptors.
type staticSource map[string]map[string]core.Descriptor

// Output implements artifactSource.
func (s staticSource) Output(stage, artifact string) (core.Descriptor, bool) {
	descriptor, ok := s[stage][artifact]
	return descriptor, ok
}

func TestStagingRunnerMaterializesVerifiedArtifact(t *testing.T) {
	store := newTestCacheStore()
	descriptor, err := store.Put(strings.NewReader("artifact bytes"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	inner := &recordingRunner{root: root}
	runner := NewStagingRunner(inner, root, store, staticSource{
		"compile": {"binary": descriptor},
	})
	stage := core.Stage{
		ID:        "package",
		DependsOn: []string{"compile"},
		Command:   core.Command{Executable: "true"},
		Inputs:    []core.ArtifactReference{{Stage: "compile", Name: "binary", Target: "/in/binary"}},
	}
	if err := runner.Run(context.Background(), stage); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "in", "binary"))
	if err != nil || string(data) != "artifact bytes" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestStagingRunnerDefaultsTargetPath(t *testing.T) {
	store := newTestCacheStore()
	descriptor, err := store.Put(strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	runner := NewStagingRunner(&recordingRunner{root: root}, root, store, staticSource{
		"build": {"out": descriptor},
	})
	stage := core.Stage{
		ID:      "use",
		Command: core.Command{Executable: "true"},
		Inputs:  []core.ArtifactReference{{Stage: "build", Name: "out"}},
	}
	if err := runner.Run(context.Background(), stage); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "inputs", "build", "out")); err != nil {
		t.Fatal(err)
	}
}

func TestStagingRunnerDetectsTamperedArtifact(t *testing.T) {
	store := newTestCacheStore()
	descriptor, err := store.Put(strings.NewReader("trusted content"))
	if err != nil {
		t.Fatal(err)
	}
	store.corrupt(descriptor.Digest, []byte("tampered content!"))
	root := t.TempDir()
	runner := NewStagingRunner(&recordingRunner{root: root}, root, store, staticSource{
		"build": {"out": descriptor},
	})
	stage := core.Stage{
		ID:      "use",
		Command: core.Command{Executable: "true"},
		Inputs:  []core.ArtifactReference{{Stage: "build", Name: "out", Target: "/in/out"}},
	}
	err = runner.Run(context.Background(), stage)
	if err == nil || !strings.Contains(err.Error(), "does not match the producer") {
		t.Fatalf("err=%v", err)
	}
}

func TestStagingRunnerRejectsMissingProducer(t *testing.T) {
	store := newTestCacheStore()
	root := t.TempDir()
	runner := NewStagingRunner(&recordingRunner{root: root}, root, store, staticSource{})
	stage := core.Stage{
		ID:      "use",
		Command: core.Command{Executable: "true"},
		Inputs:  []core.ArtifactReference{{Stage: "build", Name: "out"}},
	}
	if err := runner.Run(context.Background(), stage); err == nil ||
		!strings.Contains(err.Error(), "has not published artifact") {
		t.Fatalf("err=%v", err)
	}
}

func TestStagingRunnerRejectsMissingCachedBlob(t *testing.T) {
	store := newTestCacheStore()
	descriptor, err := store.Put(strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	store.evict(descriptor.Digest)
	root := t.TempDir()
	runner := NewStagingRunner(&recordingRunner{root: root}, root, store, staticSource{
		"build": {"out": descriptor},
	})
	err = runner.Run(context.Background(), core.Stage{
		ID: "consume", Command: core.Command{Executable: "true"},
		Inputs: []core.ArtifactReference{{Stage: "build", Name: "out", Target: "/in/out"}},
	})
	if err == nil || !strings.Contains(err.Error(), "read artifact") || !strings.Contains(err.Error(), "blob not found") {
		t.Fatalf("err=%v", err)
	}
}

func TestStagingRunnerRejectsDestinationBelowRegularFile(t *testing.T) {
	store := newTestCacheStore()
	descriptor, err := store.Put(strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "in"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := NewStagingRunner(&recordingRunner{root: root}, root, store, staticSource{
		"build": {"out": descriptor},
	})
	err = runner.Run(context.Background(), core.Stage{
		ID: "consume", Command: core.Command{Executable: "true"},
		Inputs: []core.ArtifactReference{{Stage: "build", Name: "out", Target: "/in/out"}},
	})
	if err == nil || !strings.Contains(err.Error(), "prepare /in/out") {
		t.Fatalf("err=%v", err)
	}
}

func TestStagingRunnerDoesNotOverwriteDirectory(t *testing.T) {
	store := newTestCacheStore()
	descriptor, err := store.Put(strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	destination := filepath.Join(root, "in", "out")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "sentinel"), []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := NewStagingRunner(&recordingRunner{root: root}, root, store, staticSource{
		"build": {"out": descriptor},
	})
	err = runner.Run(context.Background(), core.Stage{
		ID: "consume", Command: core.Command{Executable: "true"},
		Inputs: []core.ArtifactReference{{Stage: "build", Name: "out", Target: "/in/out"}},
	})
	if err == nil || !strings.Contains(err.Error(), "install /in/out") {
		t.Fatalf("err=%v", err)
	}
	if data, readErr := os.ReadFile(filepath.Join(destination, "sentinel")); readErr != nil || string(data) != "preserve" {
		t.Fatalf("sentinel=%q err=%v", data, readErr)
	}
}

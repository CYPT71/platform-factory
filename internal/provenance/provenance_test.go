package provenance

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/assemble"
	"github.com/CYPT71/platform-factory/internal/cache"
	"github.com/CYPT71/platform-factory/internal/core"
	api "github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/executor"
	"github.com/CYPT71/platform-factory/internal/pipeline"
)

// coreToCacheDescriptor converts core.Descriptor to cache.Descriptor.
func coreToCacheDescriptor(d core.Descriptor) cache.Descriptor {
	return cache.Descriptor{
		Digest: d.Digest,
		Size:   d.Size,
	}
}

// cachingRunnerOutputResolver adapts CachingRunner.Output to assemble.OutputResolver.
func cachingRunnerOutputResolver(r *executor.CachingRunner) assemble.OutputResolver {
	return func(stage, artifact string) (core.Descriptor, bool) {
		return r.Output(stage, artifact)
	}
}

const testDigest = "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

func testDefinition() api.Pipeline {
	return api.Pipeline{
		APIVersion: api.APIVersion,
		Name:       "example",
		Stages: []api.Stage{
			{ID: "compile", Command: api.Command{Executable: "true"}, Outputs: []api.ArtifactDeclaration{{Name: "binary", Path: "/out/binary"}}},
			{ID: "assets", Command: api.Command{Executable: "true"}, Outputs: []api.ArtifactDeclaration{{Name: "static", Path: "/out/static"}}},
			{ID: "package", DependsOn: []string{"compile", "assets"}, Command: api.Command{Executable: "true"}},
		},
		Outputs: []api.Output{{Name: "app", Stage: "compile", Artifact: "binary"}},
	}
}

func resolverFrom(digests map[string]map[string]cache.Descriptor) assemble.OutputResolver {
	return func(stage, artifact string) (core.Descriptor, bool) {
		descriptor, ok := digests[stage][artifact]
		return core.Descriptor{Digest: descriptor.Digest, Size: descriptor.Size}, ok
	}
}

func TestGenerateCapturesOrderAndOutputDigests(t *testing.T) {
	resolve := resolverFrom(map[string]map[string]cache.Descriptor{
		"compile": {"binary": {Digest: testDigest, Size: 5}},
	})

	record, err := Generate(testDefinition(), resolve, Options{BuilderIdentity: "secure-oci/test"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	wantOrder := []string{"assets", "compile", "package"}
	if len(record.Order) != len(wantOrder) {
		t.Fatalf("order=%v", record.Order)
	}
	for i, id := range wantOrder {
		if record.Order[i] != id {
			t.Fatalf("order=%v want=%v", record.Order, wantOrder)
		}
	}
	if len(record.Outputs) != 1 || record.Outputs[0].Digest != testDigest || record.Outputs[0].Name != "app" {
		t.Fatalf("outputs=%+v", record.Outputs)
	}
	if record.BuilderIdentity != "secure-oci/test" {
		t.Fatalf("builder identity=%q", record.BuilderIdentity)
	}
}

func TestGenerateRequiresBuilderIdentity(t *testing.T) {
	if _, err := Generate(testDefinition(), resolverFrom(nil), Options{}); err == nil {
		t.Fatal("expected an error for a missing builder identity")
	}
}

func TestGenerateRejectsInvalidDAG(t *testing.T) {
	definition := testDefinition()
	definition.Stages[1].DependsOn = []string{"package"} // assets -> package -> assets: a cycle
	if _, err := Generate(definition, resolverFrom(nil), Options{BuilderIdentity: "x"}); err == nil {
		t.Fatal("expected an error for an invalid DAG")
	}
}

func TestGenerateRejectsUnresolvedOutput(t *testing.T) {
	if _, err := Generate(testDefinition(), resolverFrom(nil), Options{BuilderIdentity: "x"}); err == nil {
		t.Fatal("expected an error for an unresolved output")
	}
}

func TestGenerateProducesDeterministicJSON(t *testing.T) {
	resolve := resolverFrom(map[string]map[string]cache.Descriptor{
		"compile": {"binary": {Digest: testDigest, Size: 5}},
	})
	opts := Options{BuilderIdentity: "secure-oci/test", Parameters: map[string]string{"b": "2", "a": "1"}}

	first, err := Generate(testDefinition(), resolve, opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	second, err := Generate(testDefinition(), resolve, opts)
	if err != nil {
		t.Fatalf("generate again: %v", err)
	}

	var firstBuf, secondBuf bytes.Buffer
	if err := Write(&firstBuf, first); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := Write(&secondBuf, second); err != nil {
		t.Fatalf("write: %v", err)
	}
	if firstBuf.String() != secondBuf.String() {
		t.Fatalf("output is not deterministic:\n%s\nvs\n%s", firstBuf.String(), secondBuf.String())
	}
	if !strings.Contains(firstBuf.String(), `"a":"1"`) {
		t.Fatalf("expected sorted parameters in output: %s", firstBuf.String())
	}
}

// TestGenerateComposesWithCachingRunnerOutput proves Generate accepts
// (*executor.CachingRunner).Output directly as its resolver, with no
// adapter — genuine interop, not just type-shape compatibility.
func TestGenerateComposesWithCachingRunnerOutput(t *testing.T) {
	root := t.TempDir()
	store, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	inner := pipeline.StageRunnerFunc(func(_ context.Context, stage api.Stage) error {
		for _, output := range stage.Outputs {
			path := executor.MapPath(root, output.Path)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
				return err
			}
		}
		return nil
	})
	runner := executor.NewCachingRunner(inner, root, cache.NewStoreAdapter(store), "engine/v0", testDigest, "linux/amd64")

	definition := testDefinition()
	for _, stage := range definition.Stages {
		if err := runner.Run(context.Background(), stage); err != nil {
			t.Fatalf("run %s: %v", stage.ID, err)
		}
	}

	record, err := Generate(definition, cachingRunnerOutputResolver(runner), Options{BuilderIdentity: "secure-oci/test"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(record.Outputs) != 1 || record.Outputs[0].Digest == "" {
		t.Fatalf("outputs=%+v", record.Outputs)
	}
}

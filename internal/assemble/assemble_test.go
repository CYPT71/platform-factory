package assemble

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/cache"
	"github.com/CYPT71/secure-oci-base/internal/core"
	"github.com/CYPT71/secure-oci-base/internal/layout"
	"github.com/CYPT71/secure-oci-base/internal/oci"
)

func newTestStore(t *testing.T) *cache.Store {
	t.Helper()
	store, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	return store
}

func put(t *testing.T, store *cache.Store, content string) cache.Descriptor {
	t.Helper()
	descriptor, err := store.Put(strings.NewReader(content))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	return descriptor
}

// cacheToCoreDescriptor converts cache.Descriptor to core.Descriptor.
func cacheToCoreDescriptor(d cache.Descriptor) core.Descriptor {
	return core.Descriptor{
		Digest: d.Digest,
		Size:   d.Size,
	}
}

func resolverFrom(index map[string]map[string]cache.Descriptor) OutputResolver {
	return func(stage, artifact string) (core.Descriptor, bool) {
		descriptor, ok := index[stage][artifact]
		return cacheToCoreDescriptor(descriptor), ok
	}
}

func TestExtractWritesDeclaredOutputs(t *testing.T) {
	store := newTestStore(t)
	binaryDesc := put(t, store, "binary content")
	configDesc := put(t, store, "config content")

	definition := core.Pipeline{
		Outputs: []core.Output{
			{Name: "app", Stage: "build", Artifact: "binary"},
			{Name: "config", Stage: "build", Artifact: "config"},
		},
	}
	resolve := resolverFrom(map[string]map[string]cache.Descriptor{
		"build": {"binary": binaryDesc, "config": configDesc},
	})

	dir := t.TempDir()
	paths, err := Extract(cache.NewStoreAdapter(store), definition, resolve, dir)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths=%v", paths)
	}
	data, err := os.ReadFile(paths["app"])
	if err != nil || string(data) != "binary content" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	info, err := os.Stat(paths["config"])
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
}

func TestExtractFailsForUnresolvedOutput(t *testing.T) {
	store := newTestStore(t)
	definition := core.Pipeline{Outputs: []core.Output{{Name: "app", Stage: "build", Artifact: "binary"}}}
	resolve := resolverFrom(nil)

	if _, err := Extract(cache.NewStoreAdapter(store), definition, resolve, t.TempDir()); err == nil {
		t.Fatal("expected an error for an unresolved output")
	}
}

func TestExtractFailsForMissingBlob(t *testing.T) {
	store := newTestStore(t)
	definition := core.Pipeline{Outputs: []core.Output{{Name: "app", Stage: "build", Artifact: "binary"}}}
	// A descriptor pointing at content that was never Put into this store.
	missing := cache.Descriptor{Digest: "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}
	resolve := resolverFrom(map[string]map[string]cache.Descriptor{"build": {"binary": missing}})

	if _, err := Extract(cache.NewStoreAdapter(store), definition, resolve, t.TempDir()); err == nil {
		t.Fatal("expected an error for a descriptor absent from the store")
	}
}

func testPipelineWithBinaryAndConfig() core.Pipeline {
	return core.Pipeline{
		Outputs: []core.Output{
			{Name: "app", Stage: "build", Artifact: "binary"},
			{Name: "config", Stage: "build", Artifact: "config"},
		},
	}
}

func TestImageBuildsVerifiableOCILayout(t *testing.T) {
	store := newTestStore(t)
	resolve := resolverFrom(map[string]map[string]cache.Descriptor{
		"build": {
			"binary": put(t, store, "arbitrary binary bytes"),
			"config": put(t, store, "arbitrary config bytes"),
		},
	})

	output := filepath.Join(t.TempDir(), "layout")
	digest, err := Image(cache.NewStoreAdapter(store), testPipelineWithBinaryAndConfig(), resolve, "app",
		map[string]string{"config": "/etc/app/config.txt"},
		oci.Options{Output: output},
	)
	if err != nil {
		t.Fatalf("image: %v", err)
	}
	if digest == "" {
		t.Fatal("expected a non-empty manifest digest")
	}

	report, err := layout.Verify(output)
	if err != nil {
		t.Fatalf("the assembled image did not pass independent layout verification: %v", err)
	}
	if !report.Valid || report.Manifests != 1 {
		t.Fatalf("report=%+v", report)
	}
}

func TestImageFailsWhenBinaryOutputMissing(t *testing.T) {
	store := newTestStore(t)
	resolve := resolverFrom(map[string]map[string]cache.Descriptor{
		"build": {"binary": put(t, store, "content")},
	})
	definition := core.Pipeline{Outputs: []core.Output{{Name: "app", Stage: "build", Artifact: "binary"}}}

	_, err := Image(cache.NewStoreAdapter(store), definition, resolve, "does-not-exist", nil, oci.Options{Output: filepath.Join(t.TempDir(), "layout")})
	if err == nil {
		t.Fatal("expected an error when binaryOutput names an undeclared output")
	}
}

func TestImageFailsWhenExtraFileOutputMissing(t *testing.T) {
	store := newTestStore(t)
	resolve := resolverFrom(map[string]map[string]cache.Descriptor{
		"build": {"binary": put(t, store, "content")},
	})
	definition := core.Pipeline{Outputs: []core.Output{{Name: "app", Stage: "build", Artifact: "binary"}}}

	_, err := Image(cache.NewStoreAdapter(store), definition, resolve, "app",
		map[string]string{"does-not-exist": "/etc/x"},
		oci.Options{Output: filepath.Join(t.TempDir(), "layout")},
	)
	if err == nil {
		t.Fatal("expected an error when an extra-file output is not declared")
	}
}

func TestExtractRejectsDuplicateOutputNames(t *testing.T) {
	// A regression guard: writeFile uses O_EXCL, so two declared outputs
	// resolving to the same local filename must fail loudly rather than
	// silently overwrite one another.
	store := newTestStore(t)
	descriptor := put(t, store, "content")
	definition := core.Pipeline{Outputs: []core.Output{
		{Name: "same", Stage: "a", Artifact: "x"},
		{Name: "same", Stage: "b", Artifact: "y"},
	}}
	resolve := resolverFrom(map[string]map[string]cache.Descriptor{
		"a": {"x": descriptor},
		"b": {"y": descriptor},
	})

	if _, err := Extract(cache.NewStoreAdapter(store), definition, resolve, t.TempDir()); err == nil {
		t.Fatal("expected an error for colliding output names")
	}
}

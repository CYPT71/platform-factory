package oci

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/assemble"
	"github.com/CYPT71/platform-factory/internal/cache"
	"github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/layout"
)

// TestAssembleImageBuildsVerifiableOCILayout lives here, not in
// internal/assemble, because internal/assemble is domain logic that must
// not import internal/oci (internal/archtest enforces this); internal/oci
// is the infrastructure side of that boundary and is free to depend on
// assemble to prove the two compose correctly end to end.
func TestAssembleImageBuildsVerifiableOCILayout(t *testing.T) {
	store, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	binary, err := store.Put(strings.NewReader("arbitrary binary bytes"))
	if err != nil {
		t.Fatalf("put binary: %v", err)
	}
	config, err := store.Put(strings.NewReader("arbitrary config bytes"))
	if err != nil {
		t.Fatalf("put config: %v", err)
	}
	descriptors := map[string]map[string]cache.Descriptor{
		"build": {"binary": binary, "config": config},
	}
	resolve := func(stage, artifact string) (core.Descriptor, bool) {
		descriptor, ok := descriptors[stage][artifact]
		return core.Descriptor{Digest: descriptor.Digest, Size: descriptor.Size}, ok
	}
	definition := core.Pipeline{Outputs: []core.Output{
		{Name: "app", Stage: "build", Artifact: "binary"},
		{Name: "config", Stage: "build", Artifact: "config"},
	}}

	output := filepath.Join(t.TempDir(), "layout")
	build := func(binaryPath string, extraFiles []assemble.ExtraFile) (string, error) {
		opts := Options{Output: output, Binary: binaryPath}
		for _, file := range extraFiles {
			opts.ExtraFiles = append(opts.ExtraFiles, ExtraFile{Dest: file.Dest, Source: file.Source, Mode: file.Mode})
		}
		return Build(opts)
	}

	digest, err := assemble.Image(cache.NewStoreAdapter(store), definition, resolve, "app",
		map[string]string{"config": "/etc/app/config.txt"}, build)
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

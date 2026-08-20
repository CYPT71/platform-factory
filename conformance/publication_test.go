package conformance

import (
	"testing"
	"testing/fstest"

	"github.com/CYPT71/platform-factory/internal/publicationtarget"
)

func TestRunPublicationAgainstEmbeddedVectorsAllPass(t *testing.T) {
	results, err := RunPublication(EmbeddedPublicationVectors())
	if err != nil {
		t.Fatalf("RunPublication: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one publication vector")
	}
	for _, result := range results {
		if !result.Passed {
			t.Errorf("vector %s failed: %s", result.Name, result.Detail)
		}
	}
}

func TestRunPublicationRejectsAnEmptyVectorSet(t *testing.T) {
	if _, err := RunPublication(fstest.MapFS{}); err == nil {
		t.Fatal("expected an error when no vectors-publication/*.json files exist")
	}
}

func TestRunPublicationRejectsUnknownFieldsAndMalformedJSON(t *testing.T) {
	fsys := fstest.MapFS{
		"vectors-publication/bad.json": &fstest.MapFile{Data: []byte(`{"name":"x","unexpected_field":true,"expect":{"valid":false}}`)},
	}
	if _, err := RunPublication(fsys); err == nil {
		t.Fatal("expected an error for an unknown field")
	}
}

func TestEvaluatePublicationRejectsAmbiguousTargetSelection(t *testing.T) {
	// Neither registry nor kubernetes selected.
	result := evaluatePublication(PublicationVector{Name: "neither"})
	if result.Passed {
		t.Fatalf("expected a vector selecting no target to fail: %+v", result)
	}

	// Both registry and kubernetes selected at once.
	both := evaluatePublication(PublicationVector{
		Name:       "both",
		Registry:   &RegistryPublicationInput{Reference: "registry.example/team/app:v1"},
		Kubernetes: &publicationtarget.KubernetesSpec{},
	})
	if both.Passed {
		t.Fatalf("expected a vector selecting both targets to fail: %+v", both)
	}
}

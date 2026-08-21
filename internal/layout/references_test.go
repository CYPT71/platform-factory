package layout

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetReferencesAddsSeveralImagesAndTagsWithoutDuplicatingBlobs(t *testing.T) {
	layoutPath := buildNamedLayout(t, "example/api", "v1", "amd64", "payload")
	blobDir := filepath.Join(layoutPath, "blobs", "sha256")
	before, err := os.ReadDir(blobDir)
	if err != nil {
		t.Fatal(err)
	}
	references := []string{"example/api:v1", "example/api:stable", "example/compat-api:2026.08"}
	report, err := SetReferences(layoutPath, references)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Valid || len(report.Platforms) != len(references) {
		t.Fatalf("report=%+v", report)
	}
	seen := map[string]string{}
	for _, platform := range report.Platforms {
		seen[platform.Reference] = platform.Digest
	}
	if len(seen) != len(references) || seen[references[0]] == "" || seen[references[0]] != seen[references[1]] || seen[references[1]] != seen[references[2]] {
		t.Fatalf("references/digests=%v", seen)
	}
	after, err := os.ReadDir(blobDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("blob count changed from %d to %d", len(before), len(after))
	}
}

func TestSetReferencesRejectsInvalidDuplicateAndExcessiveInputsBeforeMutation(t *testing.T) {
	for name, references := range map[string][]string{
		"missing-tag": {"example/api"},
		"digest":      {"example/api:v1@sha256:abc"},
		"duplicate":   {"example/api:v1", "example/api:v1"},
	} {
		t.Run(name, func(t *testing.T) {
			layoutPath := buildNamedLayout(t, "example/api", "v1", "amd64", "payload")
			before, err := os.ReadFile(filepath.Join(layoutPath, "index.json"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := SetReferences(layoutPath, references); err == nil {
				t.Fatal("invalid references accepted")
			}
			after, err := os.ReadFile(filepath.Join(layoutPath, "index.json"))
			if err != nil || string(after) != string(before) {
				t.Fatalf("index mutated on failure: err=%v", err)
			}
		})
	}
}

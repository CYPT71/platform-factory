package pipeline

import (
	"encoding/json"
	"strings"
	"testing"

	api "github.com/CYPT71/platform-factory/internal/core"
)

func TestFingerprintIgnoresSemanticCollectionOrder(t *testing.T) {
	first := validPipeline()
	first.Stages[0].Inputs = append(first.Stages[0].Inputs,
		api.ArtifactReference{Stage: "assets", Name: "static"})
	second := validPipeline()
	second.Stages[0].Inputs = []api.ArtifactReference{
		{Stage: "assets", Name: "static"},
		{Stage: "compile", Name: "binary"},
	}
	second.Stages[0].DependsOn[0], second.Stages[0].DependsOn[1] =
		second.Stages[0].DependsOn[1], second.Stages[0].DependsOn[0]
	second.Stages[0], second.Stages[2] = second.Stages[2], second.Stages[0]
	second.Stages[1], second.Stages[2] = second.Stages[2], second.Stages[1]

	firstDigest, err := Fingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := Fingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest || !strings.HasPrefix(firstDigest, "sha256:") {
		t.Fatalf("first=%s second=%s", firstDigest, secondDigest)
	}
}

func TestFingerprintChangesForMeaningfulInput(t *testing.T) {
	first := validPipeline()
	second := validPipeline()
	second.Stages[1].Command.Args = []string{"--production"}
	firstDigest, _ := Fingerprint(first)
	secondDigest, _ := Fingerprint(second)
	if firstDigest == secondDigest {
		t.Fatalf("digest did not change: %s", firstDigest)
	}
}

func TestCanonicalJSONIsValidAndDoesNotMutateCaller(t *testing.T) {
	definition := validPipeline()
	originalFirstStage := definition.Stages[0].ID
	data, err := CanonicalJSON(definition)
	if err != nil {
		t.Fatal(err)
	}
	var decoded api.Pipeline
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if definition.Stages[0].ID != originalFirstStage {
		t.Fatal("canonicalization mutated the caller")
	}
	if decoded.Stages[0].ID != "assets" {
		t.Fatalf("canonical stages=%v", decoded.Stages)
	}
}

func TestCanonicalJSONRejectsInvalidPipeline(t *testing.T) {
	if _, err := CanonicalJSON(api.Pipeline{}); err == nil {
		t.Fatal("expected validation error")
	}
	if _, err := Fingerprint(api.Pipeline{}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestSortedStringsCopiesInput(t *testing.T) {
	input := []string{"b", "a"}
	result := sortedStrings(input)
	if input[0] != "b" || result[0] != "a" {
		t.Fatalf("input=%v result=%v", input, result)
	}
}

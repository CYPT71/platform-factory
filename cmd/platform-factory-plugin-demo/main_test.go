package main

import (
	"context"
	"testing"

	plugin "github.com/CYPT71/secure-oci-base/sdk/plugin"
)

func TestReferenceExtensionImplementsOnlyStableContract(t *testing.T) {
	var implementation plugin.LanguageExtension = extension{}
	detected, err := implementation.Detect(context.Background(), plugin.DetectParams{Path: "."})
	if err != nil || detected.Kind != "example" {
		t.Fatalf("Detect=%+v err=%v", detected, err)
	}
	frozen, err := implementation.Freeze(context.Background(), plugin.FreezeParams{Language: "example", Root: "."})
	if err != nil || len(frozen.Steps) != 1 {
		t.Fatalf("Freeze=%+v err=%v", frozen, err)
	}
	planned, err := implementation.Plan(context.Background(), plugin.PlanParams{Language: "example", Root: "."})
	if err != nil || len(planned.Notes) != 1 {
		t.Fatalf("Plan=%+v err=%v", planned, err)
	}
}

func TestReferenceExtensionRejectsMissingRequiredInputs(t *testing.T) {
	implementation := extension{}
	if _, err := implementation.Detect(context.Background(), plugin.DetectParams{}); err == nil {
		t.Fatal("Detect accepted an empty path")
	}
	if _, err := implementation.Freeze(context.Background(), plugin.FreezeParams{}); err == nil {
		t.Fatal("Freeze accepted empty inputs")
	}
	if _, err := implementation.Plan(context.Background(), plugin.PlanParams{}); err == nil {
		t.Fatal("Plan accepted empty inputs")
	}
}

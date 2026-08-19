package status

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComputeUninitializedDirectory(t *testing.T) {
	got := Compute(t.TempDir())
	if got.Initialized || got.NextAction != "pf init" {
		t.Fatalf("got=%+v", got)
	}
}

func TestComputeInitializedProjectNextIsBuild(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pf.yaml"), []byte(
		"version: 1\nlanguage: compiled\nprofile: static\nartifact: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Compute(root)
	if !got.Initialized || got.Built || got.NextAction != "pf build" {
		t.Fatalf("got=%+v", got)
	}
}

func TestExplainReasonCoversEveryStage(t *testing.T) {
	cases := []struct {
		name   string
		status Status
		want   string
	}{
		{"uninitialized", Status{}, "no Platform Factory project"},
		{"initialized", Status{Initialized: true}, "no verified OCI build"},
		{"built-incomplete-evidence", Status{Initialized: true, Built: true}, "evidence is incomplete"},
		{"built-complete-evidence", Status{Initialized: true, Built: true, EvidenceComplete: true}, "ready for a registry target"},
		{"published", Status{Initialized: true, Built: true, EvidenceComplete: true, Published: true}, "has not been deployed"},
		{"deployed", Status{Initialized: true, Built: true, EvidenceComplete: true, Published: true, Deployed: true}, "bounded workload logs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExplainReason(tc.status)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("got=%q, want substring %q", got, tc.want)
			}
		})
	}
}

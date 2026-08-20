package project

import "testing"

func TestConfigReferenceUsesRelativePathWhenInsideRoot(t *testing.T) {
	loaded := Loaded{Root: "/repo", File: "/repo/pf.yaml"}
	if got := loaded.configReference(); got != "pf.yaml" {
		t.Fatalf("got=%q", got)
	}
}

func TestConfigReferenceFallsBackToBaseNameWhenFileIsOutsideRoot(t *testing.T) {
	loaded := Loaded{Root: "/repo/project", File: "/elsewhere/pf.yaml"}
	if got := loaded.configReference(); got != "pf.yaml" {
		t.Fatalf("got=%q", got)
	}
}

func TestHasSourceMatchesAResolvedDependency(t *testing.T) {
	loaded := Loaded{Root: "/repo"}
	dependencies := []Dependency{{Source: "vendor/lib.go"}}
	if !hasSource(dependencies, "/repo/vendor/lib.go", loaded) {
		t.Fatal("expected the resolved dependency source to match")
	}
	if hasSource(dependencies, "/repo/other.go", loaded) {
		t.Fatal("did not expect an unrelated path to match")
	}
	if hasSource(nil, "/repo/vendor/lib.go", loaded) {
		t.Fatal("expected no match against an empty dependency list")
	}
}

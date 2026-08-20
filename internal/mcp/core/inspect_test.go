package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite := func(rel, content string) {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("internal/registry/client.go", "// Package registry implements the OCI Distribution client.\npackage registry\n")
	mustWrite("internal/registry/client_test.go", "package registry\n")
	mustWrite("internal/oci/build.go", "package oci\n")
	return dir
}

func TestInspectKnownAreaReturnsItsPackages(t *testing.T) {
	dir := fixtureRepo(t)
	inspection, err := Inspect(dir, "registry")
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Packages) != 1 || inspection.Packages[0].Package != "internal/registry" {
		t.Fatalf("packages=%v", inspection.Packages)
	}
	if inspection.Packages[0].Doc != "Package registry implements the OCI Distribution client." {
		t.Fatalf("doc=%q", inspection.Packages[0].Doc)
	}
	if len(inspection.Packages[0].TestFiles) != 1 {
		t.Fatalf("testFiles=%v", inspection.Packages[0].TestFiles)
	}
}

func TestInspectUnknownAreaReturnsAnError(t *testing.T) {
	dir := fixtureRepo(t)
	if _, err := Inspect(dir, "not-a-real-area"); err == nil {
		t.Fatal("expected an error for an unknown area")
	}
}

// TestInspectAllOnARepoWithNoInternalDirectoryReturnsNoPackages covers
// allInternalPackages' own os.ReadDir error branch (no internal/
// directory at all) - it must return an empty inspection, not an
// error, since a repo with no internal/ tree is a valid (if unusual)
// input for "all".
func TestInspectAllOnARepoWithNoInternalDirectoryReturnsNoPackages(t *testing.T) {
	dir := t.TempDir()
	inspection, err := Inspect(dir, "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Packages) != 0 {
		t.Fatalf("packages=%v, want none", inspection.Packages)
	}
}

// TestInspectPackageSurfacesAReadDirFailure covers inspectPackage's own
// os.ReadDir error branch directly - Inspect itself only ever calls it
// with allInternalPackages' own real, on-disk-verified directory list,
// so this path is otherwise unreachable through Inspect.
func TestInspectPackageSurfacesAReadDirFailure(t *testing.T) {
	dir := t.TempDir()
	if _, err := inspectPackage(dir, "internal/does-not-exist"); err == nil {
		t.Fatal("expected an error for a package directory that does not exist")
	}
}

func TestInspectAllReturnsEveryInternalPackage(t *testing.T) {
	dir := fixtureRepo(t)
	inspection, err := Inspect(dir, "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Packages) != 2 {
		t.Fatalf("expected 2 packages under internal/, got %v", inspection.Packages)
	}
}

func TestInspectDefaultsToAllWhenAreaIsEmpty(t *testing.T) {
	dir := fixtureRepo(t)
	inspection, err := Inspect(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Area != "all" {
		t.Fatalf("area=%q", inspection.Area)
	}
}

func TestInspectToolHandlerRejectsAnUnknownArea(t *testing.T) {
	dir := fixtureRepo(t)
	handler := InspectToolHandler(dir)
	if _, err := handler(context.Background(), json.RawMessage(`{"area":"bogus"}`)); err == nil {
		t.Fatal("expected an error for an unknown area")
	}
}

func TestInspectToolHandlerRoundTripsAKnownArea(t *testing.T) {
	dir := fixtureRepo(t)
	handler := InspectToolHandler(dir)
	out, err := handler(context.Background(), json.RawMessage(`{"area":"registry"}`))
	if err != nil {
		t.Fatalf("handler returned an error: %v", err)
	}
	var inspection AreaInspection
	if err := json.Unmarshal([]byte(out), &inspection); err != nil {
		t.Fatalf("handler output is not valid JSON: %v (%s)", err, out)
	}
	if inspection.Area != "registry" {
		t.Fatalf("inspection=%+v", inspection)
	}
}

func TestInspectToolHandlerDefaultsToAllOnEmptyArguments(t *testing.T) {
	dir := fixtureRepo(t)
	handler := InspectToolHandler(dir)
	for _, args := range []string{``, `{}`} {
		out, err := handler(context.Background(), json.RawMessage(args))
		if err != nil {
			t.Fatalf("args=%q handler returned an error: %v", args, err)
		}
		var inspection AreaInspection
		if err := json.Unmarshal([]byte(out), &inspection); err != nil {
			t.Fatalf("args=%q handler output is not valid JSON: %v (%s)", args, err, out)
		}
		if inspection.Area != "all" {
			t.Fatalf("args=%q inspection=%+v", args, inspection)
		}
	}
}

func TestInspectToolHandlerRejectsInvalidJSON(t *testing.T) {
	dir := fixtureRepo(t)
	handler := InspectToolHandler(dir)
	if _, err := handler(context.Background(), json.RawMessage(`{not json`)); err == nil {
		t.Fatal("expected an error for malformed JSON arguments")
	}
}

func TestCoreResourceHandlerReturnsAreasAndCompatibility(t *testing.T) {
	dir := fixtureRepo(t)
	handler := CoreResourceHandler(dir)
	text, mimeType, err := handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mimeType != "application/json" {
		t.Fatalf("mimeType=%q", mimeType)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["areas"].(map[string]any)["registry"]; !ok {
		t.Fatalf("expected a registry area, got %v", payload["areas"])
	}
	if payload["compatibility"] == "" {
		t.Fatal("expected a non-empty compatibility note")
	}
}

func TestPackagesResourceHandlerListsInternalPackages(t *testing.T) {
	dir := fixtureRepo(t)
	handler := PackagesResourceHandler(dir)
	text, _, err := handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var packages []PackageInfo
	if err := json.Unmarshal([]byte(text), &packages); err != nil {
		t.Fatal(err)
	}
	if len(packages) != 2 {
		t.Fatalf("packages=%v", packages)
	}
}

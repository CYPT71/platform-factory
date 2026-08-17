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

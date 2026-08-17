package docutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPackageDocReturnsFirstDocCommentLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "widget.go"), []byte("// Package widget makes widgets.\npackage widget\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if doc := PackageDoc(dir); doc != "Package widget makes widgets." {
		t.Fatalf("doc=%q", doc)
	}
}

func TestPackageDocReturnsEmptyWithoutADocComment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plain.go"), []byte("package plain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if doc := PackageDoc(dir); doc != "" {
		t.Fatalf("doc=%q, want empty", doc)
	}
}

// TestPackageDocSkipsBuildConstraintComments guards a real bug found by
// hand while driving the live MCP server against this repository:
// packages like internal/hypervisor start with "//go:build darwin &&
// cgo" directly above "package hypervisor", and that build tag was
// being returned as if it were the package's documentation.
func TestPackageDocSkipsBuildConstraintComments(t *testing.T) {
	dir := t.TempDir()
	onlyBuildTag := "//go:build linux && amd64\n\npackage onlybuildtag\n"
	if err := os.WriteFile(filepath.Join(dir, "onlybuildtag.go"), []byte(onlyBuildTag), 0o644); err != nil {
		t.Fatal(err)
	}
	if doc := PackageDoc(dir); doc != "" {
		t.Fatalf("doc=%q, want empty for a package with only a build-tag comment", doc)
	}

	withDoc := t.TempDir()
	buildTagThenDoc := "//go:build linux\n\n// Package withdoc does real things.\npackage withdoc\n"
	if err := os.WriteFile(filepath.Join(withDoc, "withdoc.go"), []byte(buildTagThenDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	if doc := PackageDoc(withDoc); doc != "Package withdoc does real things." {
		t.Fatalf("doc=%q", doc)
	}

	legacyBuildTag := t.TempDir()
	plusBuildForm := "// +build linux\n\npackage legacy\n"
	if err := os.WriteFile(filepath.Join(legacyBuildTag, "legacy.go"), []byte(plusBuildForm), 0o644); err != nil {
		t.Fatal(err)
	}
	if doc := PackageDoc(legacyBuildTag); doc != "" {
		t.Fatalf("doc=%q, want empty for the legacy // +build form", doc)
	}
}

func TestPackageDocReturnsEmptyForAMissingDirectory(t *testing.T) {
	if doc := PackageDoc(filepath.Join(t.TempDir(), "does-not-exist")); doc != "" {
		t.Fatalf("doc=%q, want empty", doc)
	}
}

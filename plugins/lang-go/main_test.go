package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunInspectAndBuildLayerAndErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runInspect([]string{"--root", root}); err != nil {
		t.Fatalf("runInspect: %v", err)
	}
	outTar := filepath.Join(t.TempDir(), "deps.tar")
	if err := runBuildLayer([]string{"--root", root, "--output", outTar, "--dest", "app/vendor"}); err == nil {
		t.Fatal("expected build-layer to fail before vendor exists")
	}
	if err := os.MkdirAll(filepath.Join(root, "vendor", "example.com", "dep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "vendor", "example.com", "dep", "dep.go"), []byte("package dep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runBuildLayer([]string{"--root", root, "--output", outTar, "--dest", "app/vendor"}); err != nil {
		t.Fatalf("runBuildLayer: %v", err)
	}
	if _, err := os.Stat(outTar); err != nil {
		t.Fatalf("expected layer tar: %v", err)
	}
	if err := runFreeze([]string{"--root", t.TempDir()}); err == nil {
		t.Fatal("expected runFreeze to fail without go.mod")
	}
}

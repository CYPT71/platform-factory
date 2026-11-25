package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindDotnetHostDefaultsAndHint(t *testing.T) {
	root := t.TempDir()
	if _, _, err := findDotnetHost(root, ""); err == nil {
		t.Fatal("expected error when no dotnet binary exists")
	}

	dotnetDir := filepath.Join(root, "usr", "share", "dotnet")
	if err := os.MkdirAll(dotnetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dotnetDir, "dotnet"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin, dir, err := findDotnetHost(root, "")
	if err != nil || bin != "/usr/share/dotnet/dotnet" || dir != "/usr/share/dotnet" {
		t.Fatalf("got bin=%q dir=%q err=%v", bin, dir, err)
	}

	custom := filepath.Join(root, "opt", "custom-dotnet", "dotnet")
	if err := os.MkdirAll(filepath.Dir(custom), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(custom, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin, dir, err = findDotnetHost(root, "/opt/custom-dotnet/dotnet")
	if err != nil || bin != "/opt/custom-dotnet/dotnet" || dir != "/opt/custom-dotnet" {
		t.Fatalf("hint bin=%q dir=%q err=%v", bin, dir, err)
	}
	if _, _, err := findDotnetHost(root, "/opt/missing/dotnet"); err == nil {
		t.Fatal("expected error for missing hint")
	}
}

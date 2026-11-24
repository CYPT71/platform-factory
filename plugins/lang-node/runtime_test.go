package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindNodeInterpreterDefaultsAndHint(t *testing.T) {
	root := t.TempDir()
	if _, err := findNodeInterpreter(root, ""); err == nil {
		t.Fatal("expected error when no node binary exists")
	}

	usrLocalBin := filepath.Join(root, "usr", "local", "bin")
	if err := os.MkdirAll(usrLocalBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(usrLocalBin, "node"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := findNodeInterpreter(root, "")
	if err != nil || got != "/usr/local/bin/node" {
		t.Fatalf("got=%q err=%v", got, err)
	}

	custom := filepath.Join(root, "opt", "node", "bin", "node")
	if err := os.MkdirAll(filepath.Dir(custom), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(custom, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = findNodeInterpreter(root, "/opt/node/bin/node")
	if err != nil || got != "/opt/node/bin/node" {
		t.Fatalf("hint got=%q err=%v", got, err)
	}
	if _, err := findNodeInterpreter(root, "/opt/missing/node"); err == nil {
		t.Fatal("expected error for missing hint")
	}
}

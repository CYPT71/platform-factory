package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CYPT71/platform-factory/sdk/langplugin"
)

func TestParseRootFlagRequiresRoot(t *testing.T) {
	if _, err := langplugin.ParseRootFlag("freeze", nil); err == nil {
		t.Fatal("expected an error when --root is missing")
	}
	root, err := langplugin.ParseRootFlag("freeze", []string{"--root", "/tmp/project"})
	if err != nil || root != "/tmp/project" {
		t.Fatalf("root=%q err=%v", root, err)
	}
}

func TestParseBuildLayerFlagsRequiresAllThree(t *testing.T) {
	cases := [][]string{
		nil,
		{"--root", "/tmp/project"},
		{"--root", "/tmp/project", "--output", "/tmp/out.tar"},
	}
	for _, args := range cases {
		if _, _, _, err := langplugin.ParseBuildLayerFlags(args); err == nil {
			t.Fatalf("args=%v: expected an error", args)
		}
	}
	root, output, dest, err := langplugin.ParseBuildLayerFlags([]string{
		"--root", "/tmp/project", "--output", "/tmp/out.tar", "--dest", "app/deps/python",
	})
	if err != nil || root != "/tmp/project" || output != "/tmp/out.tar" || dest != "app/deps/python" {
		t.Fatalf("root=%q output=%q dest=%q err=%v", root, output, dest, err)
	}
}

func TestRunBuildLayerRequiresFreezeFirst(t *testing.T) {
	root := t.TempDir() // no .platform-factory/deps/python here
	err := runBuildLayer([]string{
		"--root", root,
		"--output", filepath.Join(t.TempDir(), "out.tar"),
		"--dest", "app/deps/python",
	})
	if err == nil {
		t.Fatal("expected an error when freeze has never run")
	}
}

// TestRunBuildLayerDelegatesToSharedTarWriter proves the plugin's own
// wiring reaches sdk/langplugin.WriteDeterministicTar correctly - the
// tar-writer's own behavior (sorting, zeroed timestamps, symlink
// rejection, determinism) is covered by sdk/langplugin's own tests, not
// duplicated here.
func TestRunBuildLayerDelegatesToSharedTarWriter(t *testing.T) {
	root := t.TempDir()
	depsDir := filepath.Join(root, depsRelPath)
	if err := os.MkdirAll(depsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depsDir, "pkg.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "out.tar")
	if err := runBuildLayer([]string{
		"--root", root,
		"--output", output,
		"--dest", "app/deps/python",
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Size() == 0 {
		t.Fatalf("output=%v err=%v", info, err)
	}
}

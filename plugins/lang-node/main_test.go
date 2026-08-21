package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/CYPT71/platform-factory/sdk/langplugin"
)

func TestInspectRecognizesTypeScriptEntrypoint(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "tsconfig.json"), []byte(`{"compilerOptions":{"target":"ES2022"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.ts"), []byte("const message: string = 'hello';\nconsole.log(message);\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	output, err := os.CreateTemp(t.TempDir(), "inspection-*.json")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = output
	err = runInspect([]string{"--root", root})
	os.Stdout = old
	if closeErr := output.Close(); err != nil || closeErr != nil {
		t.Fatalf("inspect=%v close=%v", err, closeErr)
	}
	raw, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	var inspection langplugin.Inspection
	if err := json.Unmarshal(raw, &inspection); err != nil {
		t.Fatal(err)
	}
	if !inspection.Match || inspection.Language != "node" {
		t.Fatalf("inspection=%+v", inspection)
	}
	if inspection.Artifact != "index.ts" || inspection.Entrypoint != "index.ts" {
		t.Fatalf("TypeScript entrypoint was not resolved: %+v", inspection)
	}
}

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
	if _, _, _, err := langplugin.ParseBuildLayerFlags(nil); err == nil {
		t.Fatal("expected an error")
	}
	root, output, dest, err := langplugin.ParseBuildLayerFlags([]string{
		"--root", "/tmp/project", "--output", "/tmp/out.tar", "--dest", "app/deps/node",
	})
	if err != nil || root != "/tmp/project" || output != "/tmp/out.tar" || dest != "app/deps/node" {
		t.Fatalf("root=%q output=%q dest=%q err=%v", root, output, dest, err)
	}
}

func TestRunBuildLayerRequiresFreezeFirst(t *testing.T) {
	root := t.TempDir() // no node_modules here
	err := runBuildLayer([]string{
		"--root", root,
		"--output", filepath.Join(t.TempDir(), "out.tar"),
		"--dest", "app/deps/node",
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
	if err := os.WriteFile(filepath.Join(depsDir, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "out.tar")
	if err := runBuildLayer([]string{
		"--root", root,
		"--output", output,
		"--dest", "app/deps/node",
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Size() == 0 {
		t.Fatalf("output=%v err=%v", info, err)
	}
}

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
	if _, _, _, err := langplugin.ParseBuildLayerFlags(nil); err == nil {
		t.Fatal("expected an error")
	}
	root, output, dest, err := langplugin.ParseBuildLayerFlags([]string{
		"--root", "/tmp/project", "--output", "/tmp/out.tar", "--dest", "app/deps/java",
	})
	if err != nil || root != "/tmp/project" || output != "/tmp/out.tar" || dest != "app/deps/java" {
		t.Fatalf("root=%q output=%q dest=%q err=%v", root, output, dest, err)
	}
}

func TestDetectBuildToolPrefersMavenWrapper(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "mvnw"), "x")
	mustWriteFile(t, filepath.Join(root, "gradlew"), "x")
	mustWriteFile(t, filepath.Join(root, "pom.xml"), "x")
	tool, err := detectBuildTool(root)
	if err != nil || tool != toolMavenWrapper {
		t.Fatalf("tool=%v err=%v", tool, err)
	}
}

func TestDetectBuildToolFallsBackToGradleWrapper(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "gradlew"), "x")
	tool, err := detectBuildTool(root)
	if err != nil || tool != toolGradleWrapper {
		t.Fatalf("tool=%v err=%v", tool, err)
	}
}

func TestDetectBuildToolFallsBackToBarePom(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "pom.xml"), "x")
	tool, err := detectBuildTool(root)
	if err != nil || tool != toolMavenBare {
		t.Fatalf("tool=%v err=%v", tool, err)
	}
}

func TestDetectBuildToolErrorsWhenNothingFound(t *testing.T) {
	root := t.TempDir()
	if _, err := detectBuildTool(root); err == nil {
		t.Fatal("expected an error when no build tool markers exist")
	}
}

func TestDepsRelPathForSelectsGradleOnlyForGradleWrapper(t *testing.T) {
	if got := depsRelPathFor(toolGradleWrapper); got != gradleDepsRelPath {
		t.Fatalf("got %q, want %q", got, gradleDepsRelPath)
	}
	for _, tool := range []buildTool{toolMavenWrapper, toolMavenBare} {
		if got := depsRelPathFor(tool); got != mavenDepsRelPath {
			t.Fatalf("tool=%v got %q, want %q", tool, got, mavenDepsRelPath)
		}
	}
}

func TestRunBuildLayerRequiresFreezeFirst(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "pom.xml"), "x") // build tool present, deps dir isn't
	err := runBuildLayer([]string{
		"--root", root,
		"--output", filepath.Join(t.TempDir(), "out.tar"),
		"--dest", "app/deps/java",
	})
	if err == nil {
		t.Fatal("expected an error when freeze has never run")
	}
}

func TestRunBuildLayerErrorsWithNoBuildToolDetected(t *testing.T) {
	root := t.TempDir()
	err := runBuildLayer([]string{
		"--root", root,
		"--output", filepath.Join(t.TempDir(), "out.tar"),
		"--dest", "app/deps/java",
	})
	if err == nil {
		t.Fatal("expected an error when no build tool markers exist")
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunBuildLayerDelegatesToSharedTarWriter proves the plugin's own
// wiring reaches sdk/langplugin.WriteDeterministicTar correctly - the
// tar-writer's own behavior (sorting, zeroed timestamps, symlink
// rejection, determinism) is covered by sdk/langplugin's own tests, not
// duplicated here.
func TestRunBuildLayerDelegatesToSharedTarWriter(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "pom.xml"), "x")
	depsDir := filepath.Join(root, mavenDepsRelPath)
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
		"--dest", "app/deps/java",
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Size() == 0 {
		t.Fatalf("output=%v err=%v", info, err)
	}
}

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
		"--root", "/tmp/project", "--output", "/tmp/out.tar", "--dest", "app/deps/dotnet",
	})
	if err != nil || root != "/tmp/project" || output != "/tmp/out.tar" || dest != "app/deps/dotnet" {
		t.Fatalf("root=%q output=%q dest=%q err=%v", root, output, dest, err)
	}
}

func TestRunBuildLayerRequiresFreezeFirst(t *testing.T) {
	root := t.TempDir() // no .platform-factory/deps/dotnet here
	err := runBuildLayer([]string{
		"--root", root,
		"--output", filepath.Join(t.TempDir(), "out.tar"),
		"--dest", "app/deps/dotnet",
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
		"--dest", "app/deps/dotnet",
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Size() == 0 {
		t.Fatalf("output=%v err=%v", info, err)
	}
}

func TestFreezeWithInstalledSDKAndNoExternalPackages(t *testing.T) {
	if _, err := exec.LookPath("dotnet"); err != nil {
		t.Skip("dotnet SDK unavailable")
	}
	version, err := exec.Command("dotnet", "--version").Output()
	if err != nil {
		t.Skipf("dotnet SDK unavailable: %v", err)
	}
	major, err := strconv.Atoi(strings.Split(strings.TrimSpace(string(version)), ".")[0])
	if err != nil || major < 8 {
		t.Skipf("unsupported SDK version %q", version)
	}
	root := t.TempDir()
	project := fmt.Sprintf(`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net%d.0</TargetFramework></PropertyGroup></Project>`, major)
	if err := os.WriteFile(filepath.Join(root, "Fixture.csproj"), []byte(project), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "NuGet.Config"), []byte(`<configuration><packageSources><clear /></packageSources></configuration>`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOTNET_CLI_TELEMETRY_OPTOUT", "1")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := runFreeze([]string{"--root", relative}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "packages.lock.json")); err != nil {
		t.Fatalf("lock absent: %v", err)
	}
	if err := runBuildLayer([]string{"--root", root, "--output", filepath.Join(t.TempDir(), "deps.tar"), "--dest", "app/deps/dotnet"}); err != nil {
		t.Fatal(err)
	}
}

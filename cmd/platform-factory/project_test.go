package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	projectapp "github.com/CYPT71/platform-factory/internal/app/project"
	"github.com/CYPT71/platform-factory/internal/layout"
	"github.com/CYPT71/platform-factory/internal/plugin"
	"github.com/CYPT71/platform-factory/internal/project"
	api "github.com/CYPT71/platform-factory/sdk/plugin"
)

func writeProjectTestFile(t *testing.T, name, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func loadProjectTest(t *testing.T, content string) project.Loaded {
	t.Helper()
	root := t.TempDir()
	filename := filepath.Join(root, ".config_image.yaml")
	writeProjectTestFile(t, filename, content, 0o644)
	loaded, err := project.Load(filename)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestFreezeStepsCoverBuiltInAndCustomLanguages(t *testing.T) {
	tests := map[string]string{
		"go":       "go.mod",
		"node":     "package-lock.json",
		"python":   "requirements.txt",
		"java":     "pom.xml",
		"dotnet":   "project.csproj",
		"rust":     "Cargo.toml",
		"ruby":     "Gemfile",
		"php":      "composer.json",
		"compiled": "artifact",
	}
	for language, marker := range tests {
		t.Run(language, func(t *testing.T) {
			loaded := loadProjectTest(t, "language: "+language+"\nartifact: app\n")
			writeProjectTestFile(t, filepath.Join(loaded.Root, marker), "x", 0o644)
			if _, err := freezeSteps(loaded); err != nil {
				t.Fatal(err)
			}
		})
	}
	loaded := loadProjectTest(t, "language: custom\nartifact: app\nfreeze_command: [tool, lock]\n")
	steps, err := freezeSteps(loaded)
	if err != nil || len(steps) != 1 || strings.Join(steps[0].args, " ") != "tool lock" {
		t.Fatalf("steps=%+v err=%v", steps, err)
	}
}

func TestProjectBuildRejectsChangedFrozenInputsBeforeRunningBuildCommand(t *testing.T) {
	loaded := loadProjectTest(t, "language: go\nartifact: app\ndependency_management:\n  mode: manifest\n  file: go.mod\ninclude:\n  - source: go.mod\n    destination: /src/go.mod\nbuild_command: [tool, build]\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "go.mod"), "module example.test/app\n", 0o644)
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	if _, err := loaded.WriteFreezeInventory(); err != nil {
		t.Fatal(err)
	}
	writeProjectTestFile(t, filepath.Join(loaded.Root, "go.mod"), "module example.test/changed\n", 0o644)
	called := false
	execute := func(string, []string, string, io.Writer, io.Writer) error { called = true; return nil }
	var stdout, stderr bytes.Buffer
	if _, code := buildProject(loaded, &stdout, &stderr, execute); code != 1 || !strings.Contains(stderr.String(), "run `pf freeze`") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if called {
		t.Fatal("build command ran before frozen inputs were verified")
	}
}

func TestFreezeStepVariants(t *testing.T) {
	for _, test := range []struct {
		name, language, marker, first string
	}{
		{"node-without-lock", "node", "package.json", "npm install"},
		{"python-lock", "python", "requirements.lock", "python -m pip install"},
		{"maven-wrapper", "java", "mvnw", "./mvnw"},
		{"gradle-wrapper", "java", "gradlew", "./gradlew"},
	} {
		t.Run(test.name, func(t *testing.T) {
			loaded := loadProjectTest(t, "language: "+test.language+"\nartifact: app\n")
			writeProjectTestFile(t, filepath.Join(loaded.Root, test.marker), "x", 0o755)
			steps, err := freezeSteps(loaded)
			if err != nil || len(steps) == 0 ||
				!strings.HasPrefix(strings.Join(steps[0].args, " "), test.first) {
				t.Fatalf("steps=%v err=%v", steps, err)
			}
		})
	}
}

func TestRunProjectFreezeExecutesConfiguredCommand(t *testing.T) {
	loaded := loadProjectTest(t, "language: custom\nartifact: app\nfreeze_command: [deps, freeze]\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	var command string
	execute := func(name string, args []string, directory string, _, _ io.Writer) error {
		command = name + " " + strings.Join(args, " ")
		if directory != loaded.Root {
			t.Fatalf("directory=%s", directory)
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"freeze", "--config", loaded.File}, &stdout, &stderr, execute, nil, nil)
	if code != 0 || command != "deps freeze" {
		t.Fatalf("code=%d command=%q stderr=%s", code, command, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(loaded.Root, ".platform-factory", "freeze.lock.json")); err != nil {
		t.Fatal(err)
	}
}

func TestFreezeProjectWritesToolOutputAndInventory(t *testing.T) {
	loaded := loadProjectTest(t, "language: python\nruntime: python\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "requirements.txt"), "example==1.0\n", 0o644)
	writeProjectTestFile(t, filepath.Join(loaded.Root, "python"), "runtime", 0o755)
	calls := 0
	execute := func(_ string, args []string, _ string, stdout, _ io.Writer) error {
		calls++
		if strings.Contains(strings.Join(args, " "), "pip freeze") {
			_, _ = io.WriteString(stdout, "example==1.0\n")
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := freezeProject(loaded, nil, &stdout, &stderr, execute); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	lock, err := os.ReadFile(filepath.Join(loaded.Root, "requirements.lock"))
	if err != nil || string(lock) != "example==1.0\n" {
		t.Fatalf("lock=%q err=%v", lock, err)
	}
}

func TestRunProjectBuildCreatesVerifiableLayout(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\nimage: example/project\ntag: test\nplatform: linux/arm64\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"build", "--config", loaded.File}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	report, err := layout.Verify(loaded.Output())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Platforms) != 1 || report.Platforms[0].Architecture != "arm64" {
		t.Fatalf("report=%+v", report)
	}
	releaseDir := filepath.Join(loaded.Root, ".platform-factory", "release")
	for _, relative := range []string{
		"sbom.json", "provenance.json", "reports/build.json", "reports/policy.json",
		"reports/policy-rules.json", "reports/evidence.json", "reports/summary.txt", "reports/metrics.json",
	} {
		if info, err := os.Stat(filepath.Join(releaseDir, relative)); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("release output %s: info=%v err=%v", relative, info, err)
		}
	}
	provenanceData, err := os.ReadFile(filepath.Join(releaseDir, "provenance.json"))
	if err != nil || !strings.Contains(string(provenanceData), report.Platforms[0].Digest) {
		t.Fatalf("provenance is not bound to layout digest %s: %s err=%v", report.Platforms[0].Digest, provenanceData, err)
	}
}

func TestRunProjectBuildMissingArtifactGivesPlainLanguageError(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\nimage: example/project\ntag: test\nplatform: linux/arm64\n")
	// Deliberately never create loaded.Root/app.
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"build", "--config", loaded.File}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil)
	if code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	stderrText := stderr.String()
	if !strings.Contains(stderrText, `the "artifact" field in`) || !strings.Contains(stderrText, "doesn't exist") {
		t.Fatalf("expected a plain-language artifact-not-found message, stderr=%s", stderrText)
	}
	if strings.Contains(stderrText, "stat binary") {
		t.Fatalf("expected the friendly message, not the raw stat error, stderr=%s", stderrText)
	}
}

func TestRunProjectBuildCommandRanButArtifactStillMissing(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: dist/app\nbuild_command: [\"true\"]\nimage: example/project\ntag: test\nplatform: linux/arm64\n")
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"build", "--config", loaded.File}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil)
	if code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "build_command above ran, but didn't produce that file") {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func TestRunProjectBuildEnforcesResourceBudget(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\nplatform: linux/amd64\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), strings.Repeat("x", 1<<20), 0o755)
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"build", "--max-wall-clock", "1ns", "--config", loaded.File}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil)
	if code != 1 || !strings.Contains(stderr.String(), "budget exceeded") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(loaded.Output()); !os.IsNotExist(err) {
		t.Fatalf("budget failure left layout behind: %v", err)
	}
}

func TestRunProjectBuildDryRunRejectsInvalidResourceBudgetBeforeDiscovery(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"build", "--dry-run", "--max-memory", "12MB", t.TempDir()}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil)
	if code != 2 || !strings.Contains(stderr.String(), "invalid resource budget") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func snapshotTree(t *testing.T, root string) map[string]int64 {
	t.Helper()
	snapshot := map[string]int64{}
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		snapshot[name] = info.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestProjectFreezeAndBuildDryRunAreReadOnly(t *testing.T) {
	loaded := loadProjectTest(t, "language: custom\nartifact: app\nfreeze_command: [deps, freeze]\nbuild_command: [make, app]\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	before := snapshotTree(t, loaded.Root)
	executed := false
	execute := func(string, []string, string, io.Writer, io.Writer) error {
		executed = true
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := runProject([]string{"freeze", "--dry-run", "--config", loaded.File}, &stdout, &stderr, execute, nil, nil); code != 0 {
		t.Fatalf("freeze dry-run code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"dry_run": true`) || !strings.Contains(stdout.String(), "deps") {
		t.Fatalf("stdout=%s", stdout.String())
	}
	stdout.Reset()
	if code := runProject([]string{"build", "--dry-run", "--max-memory", "64MiB", "--config", loaded.File}, &stdout, &stderr, execute, nil, nil); code != 0 {
		t.Fatalf("build dry-run code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "make") || !strings.Contains(stdout.String(), `"max_memory_bytes": 67108864`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
	if executed {
		t.Fatal("dry-run executed a command")
	}
	if after := snapshotTree(t, loaded.Root); !reflect.DeepEqual(before, after) {
		t.Fatalf("dry-run mutated the project tree: before=%v after=%v", before, after)
	}
}

func TestProjectLaunchFreezesBuildsAndRuns(t *testing.T) {
	loaded := loadProjectTest(t, "language: custom\nartifact: app\nfreeze_command: [deps, freeze]\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	var frozen []string
	execute := func(name string, args []string, _ string, _, _ io.Writer) error {
		frozen = append(frozen, name+" "+strings.Join(args, " "))
		return nil
	}
	pointBothRuntimeSocketsAtFakeEngine(t)
	var runtimeCalls [][]string
	containerExecute := func(name string, args []string, _ io.Reader, _, _ io.Writer) error {
		runtimeCalls = append(runtimeCalls, append([]string{name}, args...))
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := runProject([]string{"launch", "--config", loaded.File}, &stdout, &stderr, execute, containerExecute, nil); code != 0 {
		t.Fatalf("launch code=%d stderr=%s", code, stderr.String())
	}
	if len(frozen) != 1 || frozen[0] != "deps freeze" {
		t.Fatalf("freeze commands=%v", frozen)
	}
	if _, err := os.Stat(filepath.Join(loaded.Root, ".platform-factory", "freeze.lock.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := layout.Verify(loaded.Output()); err != nil {
		t.Fatal(err)
	}
	if len(runtimeCalls) == 0 {
		t.Fatal("launch did not run the image")
	}
	frozen = nil
	stdout.Reset()
	if code := runProject([]string{"launch", "--config", loaded.File}, &stdout, &stderr, execute, containerExecute, nil); code != 0 {
		t.Fatalf("second launch code=%d stderr=%s", code, stderr.String())
	}
	if len(frozen) != 0 {
		t.Fatalf("second launch re-froze: %v", frozen)
	}
}

func TestRunProjectMigrateDryRunAndWrite(t *testing.T) {
	root := t.TempDir()
	filename := filepath.Join(root, ".config_image.yaml")
	original := "language: compiled\nartifact: app\n"
	writeProjectTestFile(t, filename, original, 0o644)
	writeProjectTestFile(t, filepath.Join(root, "app"), "binary", 0o755)
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"migrate", root}, &stdout, &stderr, nil, nil, nil)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"applied": false`) ||
		!strings.Contains(stdout.String(), `"field": "version"`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
	if data, _ := os.ReadFile(filename); string(data) != original {
		t.Fatalf("dry-run rewrote the config: %s", data)
	}
	stdout.Reset()
	code = runProject([]string{"migrate", "--write", "--config", filename}, &stdout, &stderr, nil, nil, nil)
	if code != 0 || !strings.Contains(stdout.String(), `"applied": true`) {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	data, _ := os.ReadFile(filename)
	if !strings.Contains(string(data), "version: 1") {
		t.Fatalf("config=%s", data)
	}
	if _, err := project.Load(filename); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".migrate-") {
			t.Fatalf("temporary migration file left behind: %s", entry.Name())
		}
	}
	writeProjectTestFile(t, filename, "version: 3\nlanguage: compiled\nartifact: app\n", 0o644)
	stderr.Reset()
	if code := runProject([]string{"migrate", "--config", filename}, &stdout, &stderr, nil, nil, nil); code != 1 ||
		!strings.Contains(stderr.String(), "upgrade platform-factory") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunProjectDoesNotSuggestConfigFromCoreLanguageDetection(t *testing.T) {
	root := t.TempDir()
	writeProjectTestFile(t, filepath.Join(root, "go.mod"), "module example.test/app\n", 0o644)
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"show", root}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil)
	if code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "detected") || !strings.Contains(stderr.String(), "no project image config found") {
		t.Fatalf("core attempted language detection: %s", stderr.String())
	}
	ambiguous := t.TempDir()
	writeProjectTestFile(t, filepath.Join(ambiguous, "go.mod"), "module example.test/app\n", 0o644)
	writeProjectTestFile(t, filepath.Join(ambiguous, "package-lock.json"), "{}", 0o644)
	stderr.Reset()
	code = runProject([]string{"show", ambiguous}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil)
	if code != 1 || strings.Contains(stderr.String(), "go, node") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	empty := t.TempDir()
	stderr.Reset()
	code = runProject([]string{"show", empty}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil)
	if code != 1 || strings.Contains(stderr.String(), "detected") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestProjectPlanRemainsLanguageNeutral(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	writeProjectTestFile(t, filepath.Join(loaded.Root, "go.mod"), "module example.test/app\n", 0o644)
	var stdout, stderr bytes.Buffer
	if code := planProject(loaded, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), `"detected"`) || !strings.Contains(stdout.String(), `"valid": true`) {
		t.Fatalf("plan should validate config without core language detection: %s", stdout.String())
	}
}

func TestRunProjectRunMicroVMIsolation(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\nisolation: microvm\nports: [8080:80]\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	project := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var microVMArgs []string
	microVM := func(name string, args, _ []string, _ io.Reader, _, _ io.Writer) error {
		microVMArgs = append([]string{name}, args...)
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := runProject([]string{"run", "--config", loaded.File}, &stdout, &stderr, project, nil, microVM); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if len(microVMArgs) == 0 || !strings.Contains(strings.Join(microVMArgs, " "), ".platform-factory/image") {
		t.Fatalf("microvm args=%v", microVMArgs)
	}
}

func TestRunLaunchMicroVMIsolationFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// launch with an explicit isolation flag routes to runLaunch, which
	// requires a target; a missing one is a usage error, exercising the
	// isolation-flag branch of the dispatcher.
	if code := run([]string{"launch", "--isolation=container"}, &stdout, &stderr); code == 0 {
		t.Fatal("launch --isolation without a target should fail")
	}
}

func TestRunProjectBuildSemanticLayers(t *testing.T) {
	loaded := loadProjectTest(t, `language: compiled
artifact: app
semantic_layers: true
include:
  - {source: runtime-tree, destination: /opt/runtime, category: toolchain}
`)
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	writeProjectTestFile(t, filepath.Join(loaded.Root, "runtime-tree", "interpreter"), "runtime", 0o755)
	if _, err := loaded.WriteFreezeInventory(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"build", "--config", loaded.File}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := layout.Verify(loaded.Output()); err != nil {
		t.Fatal(err)
	}
	if got := layoutLayerCount(t, loaded.Output()); got != 2 {
		t.Fatalf("layers=%d, want 2 (toolchain, application)", got)
	}
}

func TestRunProjectShowAndContainerRun(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\nimage: example/project\ntag: run\nruntime_engine: podman\nnetwork: bridge\nports: [8080:80, 8443:443]\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	projectExecute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	if code := runProject([]string{"show", "--config", loaded.File}, &stdout, &stderr, projectExecute, nil, nil); code != 0 ||
		!strings.Contains(stdout.String(), `"runtime_engine": "podman"`) {
		t.Fatalf("show code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if code := runProject([]string{"build", "--config", loaded.File}, &stdout, &stderr, projectExecute, nil, nil); code != 0 {
		t.Fatalf("build code=%d stderr=%s", code, stderr.String())
	}
	pointBothRuntimeSocketsAtFakeEngine(t)
	var calls [][]string
	containerExecute := func(name string, args []string, _ io.Reader, _, _ io.Writer) error {
		if name != "podman" {
			t.Fatalf("runtime=%s", name)
		}
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	if code := runProject([]string{"run", "--config", loaded.File}, &stdout, &stderr, projectExecute, containerExecute, nil); code != 0 {
		t.Fatalf("run code=%d stderr=%s", code, stderr.String())
	}
	// The image load and post-load presence check now happen over the
	// podman socket directly (see internal/dockersave.SocketClient), not
	// through containerExecute - only the final `podman run` invocation
	// still goes through it.
	if len(calls) != 1 || calls[0][0] != "run" {
		t.Fatalf("calls=%v", calls)
	}
	joined := strings.Join(calls[0], " ")
	for _, want := range []string{"--network=bridge", "--publish=8080:80", "--publish=8443:443"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %s", want, joined)
		}
	}
}

func TestRunProjectRunAcceptsARuntimeOverride(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\nimage: example/project\ntag: run\nruntime_engine: podman\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	projectExecute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	if code := runProject([]string{"build", "--config", loaded.File}, &stdout, &stderr, projectExecute, nil, nil); code != 0 {
		t.Fatalf("build code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	pointBothRuntimeSocketsAtFakeEngine(t)

	var runtimesSeen []string
	containerExecute := func(name string, args []string, _ io.Reader, _, _ io.Writer) error {
		runtimesSeen = append(runtimesSeen, name)
		return nil
	}
	// A junior who just says "use docker" should get docker, even
	// though the project config itself says podman.
	if code := runProject([]string{"run", "--config", loaded.File, "--runtime", "docker"}, &stdout, &stderr, projectExecute, containerExecute, nil); code != 0 {
		t.Fatalf("run code=%d stderr=%s", code, stderr.String())
	}
	for _, name := range runtimesSeen {
		if name != "docker" {
			t.Fatalf("runtimesSeen=%v, want every call to use docker", runtimesSeen)
		}
	}

	if code := runProject([]string{"run", "--config", loaded.File, "--runtime", "bogus"}, &stdout, &stderr, projectExecute, containerExecute, nil); code != 2 {
		t.Fatalf("expected an invalid --runtime value to be rejected, code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunHasExplicitTargetDistinguishesFlagsFromAPositionalTarget(t *testing.T) {
	if runHasExplicitTarget(nil) {
		t.Fatal("expected no arguments to report no explicit target")
	}
	if runHasExplicitTarget([]string{"--runtime", "docker"}) {
		t.Fatal("expected --runtime alone (no positional arg) to report no explicit target")
	}
	if runHasExplicitTarget([]string{"--memory", "128m"}) {
		t.Fatal("expected a value-taking flag given as two args not to be mistaken for the target")
	}
	if !runHasExplicitTarget([]string{"--runtime", "docker", "myimage:latest"}) {
		t.Fatal("expected a positional image reference to report an explicit target")
	}
	if !runHasExplicitTarget([]string{"./oci-layout"}) {
		t.Fatal("expected a bare positional layout path to report an explicit target")
	}
	if runHasExplicitTarget([]string{"--watch", "--runtime", "docker"}) {
		t.Fatal("expected --watch alone (no positional arg) to report no explicit target")
	}
}

func TestRunRejectsWatchWithAnExplicitTarget(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"run", "--watch", "myimage:latest"}, &stdout, &stderr); code != 2 ||
		!strings.Contains(stderr.String(), "--watch is not valid with an explicit IMAGE/layout") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestBareRunWatchDispatchesToProjectModeWithoutARealContainerRuntime(t *testing.T) {
	// Same reasoning as TestBareRunDispatchesToProjectModeWithoutARealContainerRuntime:
	// this only proves the dispatch decision (a target-less `run --watch`
	// takes the project-mode branch, with --watch recognized as a flag
	// rather than tripping runContainer's own unknown-flag error) since
	// a real watch loop needs a real container runtime.
	oldDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldDirectory) })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"run", "--watch", "--runtime", "docker"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "pf init") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestBareRunDispatchesToProjectModeWithoutARealContainerRuntime(t *testing.T) {
	// The top-level run() dispatch always uses the real docker/podman-
	// shelling executors, so this only exercises the decision itself
	// (does the dispatch route a target-less `run` into project mode)
	// rather than a full build+run, which would need a real container
	// runtime installed. With no project in a fresh temp directory, the
	// dispatch should reach project.Discover's own "no project" error -
	// proving it took the project-mode branch, not runContainer's old
	// generic usage error - without ever touching docker/podman.
	oldDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldDirectory) })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"run", "--runtime", "docker"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "pf init") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunProjectMicroVMAndBuildOnDemand(t *testing.T) {
	// One vCPU (the project config doesn't ask for more) with a single TCP
	// forward, so - as in TestRunLaunchSelectsContainerOrMicroVM - whether
	// this dispatches to the native KVM backend or run-microvm.sh/QEMU
	// depends only on whether this host really has native KVM.
	nativeAvailable := nativeKVMAvailableForTest(t)
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\nisolation: microvm\nports: [8080:80]\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	var called bool
	microExecute := func(_ string, args, environment []string, _ io.Reader, _, _ io.Writer) error {
		called = true
		if nativeAvailable {
			if len(args) < 2 || args[0] != "microvm" || args[1] != "__run-native" || !strings.Contains(strings.Join(args, " "), "8080|80") {
				t.Fatalf("args=%v (expected native dispatch)", args)
			}
			return nil
		}
		if args[len(args)-1] != "8080" || !strings.Contains(strings.Join(environment, " "), "8080|80") {
			t.Fatalf("args=%v environment=%v (expected QEMU fallback)", args, environment)
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := runProject([]string{"run", "--config", loaded.File}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, microExecute); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !called {
		t.Fatal("microVM executor was not called")
	}
}

func TestProjectCommandFailures(t *testing.T) {
	custom := loadProjectTest(t, "language: custom\nartifact: app\nfreeze_command: [tool, lock]\nbuild_command: [tool, build]\n")
	writeProjectTestFile(t, filepath.Join(custom.Root, "app"), "binary", 0o755)
	failing := func(string, []string, string, io.Writer, io.Writer) error { return errors.New("failed") }
	var stdout, stderr bytes.Buffer
	if code := freezeProject(custom, nil, &stdout, &stderr, failing); code != 1 {
		t.Fatalf("freeze code=%d", code)
	}
	if _, code := buildProject(custom, &stdout, &stderr, failing); code != 1 {
		t.Fatalf("build code=%d", code)
	}
	custom.Config.BuildCommand = nil
	custom.Config.Artifact = "missing"
	if _, code := buildProject(custom, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }); code != 1 {
		t.Fatalf("missing artifact code=%d", code)
	}
	unknown := loadProjectTest(t, "language: cobol\nartifact: app\n")
	if _, err := freezeSteps(unknown); err == nil {
		t.Fatal("expected missing adapter error")
	}
	invalidPython := loadProjectTest(t, "language: python\nruntime: python\n")
	if _, err := freezeSteps(invalidPython); err == nil {
		t.Fatal("expected missing requirements error")
	}
	for language, want := range map[string]string{
		"python": "python", "nodejs": "node", "java": "java", "dotnet": "dotnet", "compiled": "static",
	} {
		if got := projectapp.Profile(language); got != want {
			t.Fatalf("profile(%s)=%s", language, got)
		}
	}
}

func TestTopLevelProjectDispatchAndUnsupportedAction(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	for _, args := range [][]string{
		{"project", "show", "--config", loaded.File},
		{"freeze", "--config", loaded.File},
		{"plan", "--config", loaded.File},
		{"launch", "--dry-run", "--config", loaded.File},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runProject([]string{"unsupported", "--config", loaded.File}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil); code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if code := runProject([]string{"show", loaded.Root, loaded.Root}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil); code != 2 {
		t.Fatalf("too many arguments code=%d", code)
	}
}

func TestProjectPlanIsReadOnlyAndExplainsExecution(t *testing.T) {
	loaded := loadProjectTest(t, `language: custom
artifact: app
image: example/planned
tag: v1
freeze_command: [deps, lock]
build_command: [builder, release]
isolation: microvm
ports: [8080:80]
`)
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error {
		t.Fatal("plan executed a command")
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := runProject([]string{"plan", "--config", loaded.File}, &stdout, &stderr, execute, nil, nil); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{
		`"api_version": "platform-factory.dev/project-plan/v1"`,
		`"image": "example/planned:v1"`,
		`"isolation": "microvm"`,
		`"deps"`,
		`"builder"`,
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("plan missing %s: %s", want, stdout.String())
		}
	}
	for _, path := range []string{
		filepath.Join(loaded.Root, ".platform-factory", "freeze.lock.json"),
		loaded.Output(),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("plan mutated %s: %v", path, err)
		}
	}
}

func TestProjectLaunchAction(t *testing.T) {
	for _, test := range []struct {
		args       []string
		action     string
		remaining  []string
		shouldFail bool
	}{
		{args: []string{"--config", "project.yml"}, action: "launch", remaining: []string{"--config", "project.yml"}},
		{args: []string{"--dry-run", "."}, action: "plan", remaining: []string{"."}},
		{args: []string{".", "--plan"}, action: "plan", remaining: []string{"."}},
		{args: []string{"--plan", "--dry-run"}, shouldFail: true},
	} {
		action, remaining, err := projectLaunchAction(test.args)
		if (err != nil) != test.shouldFail || action != test.action ||
			!reflect.DeepEqual(remaining, test.remaining) {
			t.Fatalf("args=%v action=%q remaining=%v err=%v", test.args, action, remaining, err)
		}
	}
}

func TestRunProjectHelpAndFlagHelp(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		var stdout, stderr bytes.Buffer
		if code := runProject(args, &stdout, &stderr, nil, nil, nil); code != 0 || stdout.Len() == 0 {
			t.Fatalf("args=%v code=%d stdout=%s", args, code, stdout.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runProject([]string{"build", "--help"}, &stdout, &stderr, nil, nil, nil); code != 0 {
		t.Fatalf("build --help code=%d stderr=%s", code, stderr.String())
	}
}

// TestRunProjectSurfacesGenericDiscoverError proves that a Discover
// failure unrelated to a missing config (a malformed config file, here,
// with an explicit --config so the "suggest a config" heuristic never
// applies) surfaces as a plain failure rather than the detection-based
// suggestion.
func TestRunProjectSurfacesGenericDiscoverError(t *testing.T) {
	root := t.TempDir()
	bad := filepath.Join(root, "bad.yaml")
	writeProjectTestFile(t, bad, "language: [not valid\n", 0o644)
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"show", "--config", bad}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil)
	if code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "detected a") {
		t.Fatalf("generic discover error unexpectedly suggested a config: %s", stderr.String())
	}
}

func TestRunProjectLaunchStopsWhenFreezeFails(t *testing.T) {
	loaded := loadProjectTest(t, "language: cobol\nartifact: app\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error {
		t.Fatal("command executed after freeze failed to resolve steps")
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"launch", "--config", loaded.File}, &stdout, &stderr, execute, nil, nil)
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(loaded.Output()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("launch built despite a failed freeze: %v", err)
	}
}

func TestExplainProjectActionSurfacesFreezeResolutionError(t *testing.T) {
	loaded := loadProjectTest(t, "language: cobol\nartifact: app\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"freeze", "--dry-run", "--config", loaded.File}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil)
	if code != 2 || !strings.Contains(stderr.String(), "no built-in freeze adapter") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunConfiguredProjectSurfacesOnDemandBuildFailure(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: missing-binary\n")
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"run", "--config", loaded.File}, &stdout, &stderr, execute, nil, nil)
	if code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestRunConfiguredProjectUpgradesNoneNetworkWhenPortsArePublished
// exercises the auto-upgrade of an unset (default "none") network to
// "bridge" once the project declares published ports, without which
// publish would be silently rejected by runContainer's network=none
// guard.
func TestRunConfiguredProjectUpgradesNoneNetworkWhenPortsArePublished(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\nports: [9090:90]\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	pointBothRuntimeSocketsAtFakeEngine(t)
	var containerArgs []string
	containerExecute := func(name string, args []string, _ io.Reader, _, _ io.Writer) error {
		containerArgs = append([]string(nil), args...)
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"run", "--config", loaded.File}, &stdout, &stderr, execute, containerExecute, nil)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	joined := strings.Join(containerArgs, " ")
	if !strings.Contains(joined, "--network=bridge") || !strings.Contains(joined, "--publish=9090:90") {
		t.Fatalf("container args=%v", containerArgs)
	}
}

func TestMigrateProjectSurfacesMissingConfigAndReadFailures(t *testing.T) {
	empty := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := runProject([]string{"migrate", empty}, &stdout, &stderr, nil, nil, nil); code != 1 {
		t.Fatalf("no config found code=%d stderr=%s", code, stderr.String())
	}
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	stdout.Reset()
	stderr.Reset()
	if code := runProject([]string{"migrate", "--config", missing}, &stdout, &stderr, nil, nil, nil); code != 1 {
		t.Fatalf("missing file code=%d stderr=%s", code, stderr.String())
	}
}

func TestMigrateProjectSurfacesCreateTempFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	root := t.TempDir()
	filename := filepath.Join(root, ".config_image.yaml")
	writeProjectTestFile(t, filename, "language: compiled\nartifact: app\n", 0o644)
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(root, 0o755)
	var stdout, stderr bytes.Buffer
	if code := runProject([]string{"migrate", "--config", filename}, &stdout, &stderr, nil, nil, nil); code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestPlanProjectSurfacesFreezeResolutionError(t *testing.T) {
	loaded := loadProjectTest(t, "language: cobol\nartifact: app\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	var stdout, stderr bytes.Buffer
	if code := planProject(loaded, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestPlanProjectDetectedBlockReflectsAmbiguousLanguagePlugins is
// planProject's half of the same fix as
// TestSuggestProjectConfigDetectsAmbiguousLanguagePluginsForADirectory:
// its own "detected" block used detect.Path directly too, so it could
// never actually surface an ambiguous multi-plugin match either. A
// go-configured project whose root ALSO carries a package.json (e.g. a
// go backend with a node-based frontend build alongside it) matches
// both the ambient go and node language plugins TestMain loads.
func TestPlanProjectDetectedBlockReflectsAmbiguousLanguagePlugins(t *testing.T) {
	loaded := loadProjectTest(t, "language: go\nartifact: app\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	writeProjectTestFile(t, filepath.Join(loaded.Root, "go.mod"), "module x\n", 0o644)
	writeProjectTestFile(t, filepath.Join(loaded.Root, "package.json"), "{}", 0o644)
	var stdout, stderr bytes.Buffer
	if code := planProject(loaded, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var result struct {
		Detected struct {
			Kind            string   `json:"kind"`
			Ambiguous       bool     `json:"ambiguous"`
			Candidates      []string `json:"candidates"`
			MatchesLanguage bool     `json:"matches_language"`
		} `json:"detected"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout=%s err=%v", stdout.String(), err)
	}
	if !result.Detected.Ambiguous || result.Detected.Kind != "ambiguous" {
		t.Fatalf("detected=%+v stdout=%s", result.Detected, stdout.String())
	}
	if len(result.Detected.Candidates) != 2 || result.Detected.Candidates[0] != "go" || result.Detected.Candidates[1] != "node" {
		t.Fatalf("candidates=%v", result.Detected.Candidates)
	}
	if result.Detected.MatchesLanguage {
		t.Fatalf("an ambiguous detection must never report matches_language=true, got %+v", result.Detected)
	}
}

func TestFreezeProjectSurfacesResolutionErrorOutputFileAndInventoryFailures(t *testing.T) {
	unsupported := loadProjectTest(t, "language: cobol\nartifact: app\n")
	writeProjectTestFile(t, filepath.Join(unsupported.Root, "app"), "binary", 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	if code := freezeProject(unsupported, nil, &stdout, &stderr, execute); code != 2 {
		t.Fatalf("resolution error code=%d stderr=%s", code, stderr.String())
	}

	pythonLoaded := loadProjectTest(t, "language: python\nruntime: python\n")
	writeProjectTestFile(t, filepath.Join(pythonLoaded.Root, "requirements.txt"), "example==1.0\n", 0o644)
	writeProjectTestFile(t, filepath.Join(pythonLoaded.Root, "python"), "runtime", 0o755)
	// A directory already occupies the freeze step's output path, so
	// opening it for writing must fail.
	if err := os.MkdirAll(filepath.Join(pythonLoaded.Root, "requirements.lock"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := freezeProject(pythonLoaded, nil, &stdout, &stderr, execute); code != 1 {
		t.Fatalf("output file error code=%d stderr=%s", code, stderr.String())
	}

	if os.Geteuid() != 0 {
		inventoryBlocked := loadProjectTest(t, "language: custom\nartifact: app\nfreeze_command: [tool, lock]\n")
		writeProjectTestFile(t, filepath.Join(inventoryBlocked.Root, "app"), "binary", 0o755)
		if err := os.Chmod(inventoryBlocked.Root, 0o500); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(inventoryBlocked.Root, 0o755)
		stdout.Reset()
		stderr.Reset()
		if code := freezeProject(inventoryBlocked, nil, &stdout, &stderr, execute); code != 1 {
			t.Fatalf("inventory write error code=%d stderr=%s", code, stderr.String())
		}
	}
}

func TestFreezeStepsJavaAndCustomWithoutMarkersFail(t *testing.T) {
	javaLoaded := loadProjectTest(t, "language: java\nartifact: app\n")
	if _, err := freezeSteps(javaLoaded); err == nil {
		t.Fatal("expected java freeze without Maven/Gradle files to fail")
	}
	customLoaded := loadProjectTest(t, "language: custom\nartifact: app\n")
	if _, err := freezeSteps(customLoaded); err == nil {
		t.Fatal("expected custom language without freeze_command to fail")
	}
}

func TestBuildProjectSurfacesImageFilesAndPlatformFailures(t *testing.T) {
	badInclude := loadProjectTest(t, `language: compiled
artifact: app
include:
  - {source: missing-dependency, destination: /app/dep}
`)
	writeProjectTestFile(t, filepath.Join(badInclude.Root, "app"), "binary", 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	if _, code := buildProject(badInclude, &stdout, &stderr, execute); code != 1 {
		t.Fatalf("missing include code=%d stderr=%s", code, stderr.String())
	}

	badPlatform := loadProjectTest(t, "language: compiled\nartifact: app\n")
	writeProjectTestFile(t, filepath.Join(badPlatform.Root, "app"), "binary", 0o755)
	badPlatform.Config.Platform = "bogus"
	stdout.Reset()
	stderr.Reset()
	if _, code := buildProject(badPlatform, &stdout, &stderr, execute); code != 2 {
		t.Fatalf("bad platform code=%d stderr=%s", code, stderr.String())
	}
}

func TestBuildProjectRequiresAndVerifiesFreezeForAdditionalSources(t *testing.T) {
	loaded := loadProjectTest(t, `language: compiled
artifact: app
include:
  - {source: assets/config.json, destination: /app/config.json}
shared_deps:
  - {source: shared/model.dat, destination: /opt/model.dat}
`)
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	writeProjectTestFile(t, filepath.Join(loaded.Root, "assets", "config.json"), "{}", 0o644)
	writeProjectTestFile(t, filepath.Join(loaded.Root, "shared", "model.dat"), "model-v1", 0o644)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	if _, code := buildProject(loaded, &stdout, &stderr, execute); code != 1 || !strings.Contains(stderr.String(), "run `pf freeze`") {
		t.Fatalf("unfrozen sources code=%d stderr=%s", code, stderr.String())
	}
	if _, err := loaded.WriteFreezeInventory(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loaded.Root, "shared", "model.dat"), []byte("model-v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if _, code := buildProject(loaded, &stdout, &stderr, execute); code != 1 || !strings.Contains(stderr.String(), "changed after") {
		t.Fatalf("changed source code=%d stderr=%s", code, stderr.String())
	}
	if _, err := loaded.WriteFreezeInventory(); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if _, code := buildProject(loaded, &stdout, &stderr, execute); code != 0 {
		t.Fatalf("frozen build code=%d stderr=%s", code, stderr.String())
	}
	if _, err := layout.Verify(loaded.Output()); err != nil {
		t.Fatalf("multi-source layout invalid: %v", err)
	}
}

func TestBuildProjectValidatesPersistedDAGBeforeExecuting(t *testing.T) {
	for name, dag := range map[string]string{
		"cycle":         `{"api_version":"platform-factory.dev/v1","name":"project-build","stages":[{"id":"a","depends_on":["b"],"command":{"executable":"true"}},{"id":"b","depends_on":["a"],"command":{"executable":"true"}}]}`,
		"unknown field": `{"api_version":"platform-factory.dev/v1","name":"project-build","unknown":true,"stages":[{"id":"build","command":{"executable":"true"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			loaded := loadProjectTest(t, "language: compiled\nartifact: app\nbuild_command: [tool, build]\n")
			writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
			writeProjectTestFile(t, filepath.Join(loaded.Root, ".pf", "build.pipeline.json"), dag, 0o600)
			executed := false
			execute := func(string, []string, string, io.Writer, io.Writer) error { executed = true; return nil }
			var stdout, stderr bytes.Buffer
			if _, code := buildProject(loaded, &stdout, &stderr, execute); code != 1 || !strings.Contains(stderr.String(), "invalid build DAG") {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
			if executed {
				t.Fatal("build command ran before DAG validation")
			}
			if _, err := os.Stat(loaded.Output()); !os.IsNotExist(err) {
				t.Fatalf("invalid DAG produced output: %v", err)
			}
		})
	}
}

func TestBuildProjectRejectsSymlinkDAGAndSupportsLegacyAbsence(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	outside := filepath.Join(t.TempDir(), "pipeline.json")
	writeProjectTestFile(t, outside, `{"api_version":"platform-factory.dev/v1","name":"project-build","stages":[{"id":"build","command":{"executable":"true"}}]}`, 0o600)
	if err := os.MkdirAll(filepath.Join(loaded.Root, ".pf"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(loaded.Root, ".pf", "build.pipeline.json")); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if _, code := buildProject(loaded, &stdout, &stderr, func(string, []string, string, io.Writer, io.Writer) error { return nil }); code != 1 || !strings.Contains(stderr.String(), "symlink") {
		t.Fatalf("symlink code=%d stderr=%s", code, stderr.String())
	}
	if err := os.Remove(filepath.Join(loaded.Root, ".pf", "build.pipeline.json")); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if _, code := buildProject(loaded, &stdout, &stderr, func(string, []string, string, io.Writer, io.Writer) error { return nil }); code != 0 {
		t.Fatalf("legacy project without persisted DAG code=%d stderr=%s", code, stderr.String())
	}
}

// TestBuildProjectUsesRuntimeEntrypointDefault proves that when the
// project config sets Runtime (not Entrypoint), the default entrypoint is
// derived under /runtime/ rather than /app/.
func TestBuildProjectUsesRuntimeEntrypointDefault(t *testing.T) {
	loaded := loadProjectTest(t, "language: python\nruntime: python\nargs: [main.py]\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "python"), "runtime", 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	if _, code := buildProject(loaded, &stdout, &stderr, execute); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	blobs, err := filepath.Glob(filepath.Join(loaded.Output(), "blobs", "sha256", "*"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, blob := range blobs {
		data, err := os.ReadFile(blob)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "/runtime/python") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected the runtime-derived entrypoint /runtime/python in the built config")
	}
}

func TestRunAutoFreezesWhenAFrozenInputChangesInsteadOfFailing(t *testing.T) {
	// language: custom with an explicit include (rather than
	// dependency_management) is enough to make projectRequiresFrozenInputs
	// true, the same shape a real python project's runtime include list
	// triggers - editing dep.txt here is standing in for a developer
	// editing their own source file while `pf run`/--watch is in use.
	loaded := loadProjectTest(t, "language: custom\nartifact: app\nfreeze_command: [true]\ninclude:\n  - {source: dep.txt, destination: /app/dep.txt}\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	writeProjectTestFile(t, filepath.Join(loaded.Root, "dep.txt"), "v1", 0o644)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	pointBothRuntimeSocketsAtFakeEngine(t)
	containerExecute := func(_ string, _ []string, _ io.Reader, _, _ io.Writer) error {
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := runProject([]string{"run", "--config", loaded.File}, &stdout, &stderr, execute, containerExecute, nil); code != 0 {
		t.Fatalf("first run code=%d stderr=%s", code, stderr.String())
	}
	if err := loaded.VerifyFreezeInventory(); err != nil {
		t.Fatalf("expected the first run to have frozen automatically: %v", err)
	}

	writeProjectTestFile(t, filepath.Join(loaded.Root, "dep.txt"), "v2", 0o644)
	if err := loaded.VerifyFreezeInventory(); err == nil {
		t.Fatal("expected the freeze to be stale after changing a frozen input")
	}

	if code := runProject([]string{"run", "--config", loaded.File}, &stdout, &stderr, execute, containerExecute, nil); code != 0 {
		t.Fatalf("second run (after editing a frozen input) code=%d stderr=%s", code, stderr.String())
	}
	if err := loaded.VerifyFreezeInventory(); err != nil {
		t.Fatalf("expected the freeze to have been refreshed automatically: %v", err)
	}
}

func TestProjectNeedsRebuildDetectsMissingAndStaleLayouts(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\n")
	binary := filepath.Join(loaded.Root, "app")
	writeProjectTestFile(t, binary, "binary", 0o755)

	if rebuild, err := projectapp.NeedsRebuild(loaded); err != nil || !rebuild {
		t.Fatalf("expected a missing layout to need a rebuild: rebuild=%v err=%v", rebuild, err)
	}

	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	if code := runProject([]string{"build", "--config", loaded.File}, &stdout, &stderr, execute, nil, nil); code != 0 {
		t.Fatalf("build code=%d stderr=%s", code, stderr.String())
	}
	if rebuild, err := projectapp.NeedsRebuild(loaded); err != nil || rebuild {
		t.Fatalf("expected a fresh layout not to need a rebuild: rebuild=%v err=%v", rebuild, err)
	}

	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(binary, future, future); err != nil {
		t.Fatal(err)
	}
	if rebuild, err := projectapp.NeedsRebuild(loaded); err != nil || !rebuild {
		t.Fatalf("expected a layout older than its binary to need a rebuild: rebuild=%v err=%v", rebuild, err)
	}
}

// watchContainerExecuteStub simulates docker/podman for
// runConfiguredProjectWatch: "image" always reports present (so
// prepareContainerImage never needs a real import), "run" blocks until
// a matching "stop" call arrives (like a real attached `docker run
// --rm` blocks until the container stops), and every call is recorded
// for assertions.
type watchContainerExecuteStub struct {
	mu      sync.Mutex
	running map[string]chan struct{}
	calls   []string
}

func newWatchContainerExecuteStub() *watchContainerExecuteStub {
	return &watchContainerExecuteStub{running: map[string]chan struct{}{}}
}

func (s *watchContainerExecuteStub) execute(_ string, args []string, stdin io.Reader, _, _ io.Writer) error {
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "image":
		return nil
	case "load":
		_, err := io.Copy(io.Discard, stdin)
		return err
	case "run":
		name := ""
		for _, arg := range args {
			if rest, ok := strings.CutPrefix(arg, "--name="); ok {
				name = rest
			}
		}
		stop := make(chan struct{})
		s.mu.Lock()
		s.running[name] = stop
		s.calls = append(s.calls, "run:"+name)
		s.mu.Unlock()
		<-stop
		return nil
	case "stop":
		name := args[1]
		s.mu.Lock()
		if stop, ok := s.running[name]; ok {
			close(stop)
			delete(s.running, name)
		}
		s.calls = append(s.calls, "stop:"+name)
		s.mu.Unlock()
		return nil
	}
	return nil
}

func (s *watchContainerExecuteStub) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// syncBuffer is bytes.Buffer plus a mutex: runConfiguredProjectWatch
// writes progress lines from its own goroutine while the test
// concurrently reads them for a failure message, which a plain
// bytes.Buffer does not allow safely.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestRunConfiguredProjectWatchRebuildsOnChangeAndStopsOnCancel(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\nimage: example/watch\ntag: dev\n")
	binary := filepath.Join(loaded.Root, "app")
	writeProjectTestFile(t, binary, "binary", 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	pointBothRuntimeSocketsAtFakeEngine(t)

	stub := newWatchContainerExecuteStub()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	go func() {
		done <- runConfiguredProjectWatch(ctx, loaded, nil, stdout, stderr, execute, stub.execute, 10*time.Millisecond)
	}()

	// Wait for the first container to actually start before mutating the
	// source - otherwise the change could be picked up by the initial
	// build instead of the watch loop's own rebuild path.
	deadline := time.Now().Add(5 * time.Second)
	for len(stub.snapshot()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the first watched container to start")
		}
		time.Sleep(time.Millisecond)
	}

	// Touch to the current wall-clock time (not an artificial future
	// timestamp): the watch loop's own rebuild also updates index.json's
	// mtime, and a fixed future timestamp would stay newer than every
	// subsequent rebuild forever, causing an infinite rebuild loop
	// instead of exactly one. The short sleep gives a filesystem with
	// coarser mtime resolution room to observe this as strictly later
	// than the first build.
	time.Sleep(20 * time.Millisecond)
	now := time.Now()
	if err := os.Chtimes(binary, now, now); err != nil {
		t.Fatal(err)
	}

	deadline = time.Now().Add(5 * time.Second)
	for {
		calls := stub.snapshot()
		if len(calls) >= 3 && strings.HasPrefix(calls[0], "run:") && strings.HasPrefix(calls[1], "stop:") && strings.HasPrefix(calls[2], "run:") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for a rebuild+restart cycle, calls=%v stderr=%s", calls, stderr.String())
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("code=%d stderr=%s", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for runConfiguredProjectWatch to return after cancel")
	}
	calls := stub.snapshot()
	if calls[len(calls)-1] == "" || !strings.HasPrefix(calls[len(calls)-1], "stop:") {
		t.Fatalf("expected the watch loop to stop its container on cancel, calls=%v", calls)
	}
}

func TestWatchContainerNameIsStableAndSafe(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\n")
	name := projectapp.WatchContainerName(loaded)
	if !validContainerName(name) {
		t.Fatalf("watchContainerName produced an invalid container name: %q", name)
	}
	if projectapp.WatchContainerName(loaded) != name {
		t.Fatal("expected watchContainerName to be stable across calls for the same project")
	}
}

func TestValidContainerName(t *testing.T) {
	valid := []string{"a", "app", "pf-watch-app", "app_1.2"}
	invalid := []string{"", "-app", ".app", "app name", "app!"}
	for _, name := range valid {
		if !validContainerName(name) {
			t.Errorf("expected %q to be valid", name)
		}
	}
	for _, name := range invalid {
		if validContainerName(name) {
			t.Errorf("expected %q to be invalid", name)
		}
	}
}

func TestRunProjectRejectsInvalidActionsAndMissingConfig(t *testing.T) {
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	for _, args := range [][]string{nil, {"unknown"}, {"show", t.TempDir()}} {
		var stdout, stderr bytes.Buffer
		if code := runProject(args, &stdout, &stderr, execute, nil, nil); code == 0 {
			t.Fatalf("args=%v unexpectedly succeeded", args)
		}
	}
}

func TestSuggestProjectConfig(t *testing.T) {
	var stderr bytes.Buffer
	code := suggestProjectConfig("run", filepath.Join(t.TempDir(), "missing"), &stderr)
	if code != 1 {
		t.Fatalf("missing path: code = %d", code)
	}
	if !strings.Contains(stderr.String(), "pf init") {
		t.Fatalf("missing path stderr = %q", stderr.String())
	}

	stderr.Reset()
	emptyDir := t.TempDir()
	code = suggestProjectConfig("run", emptyDir, &stderr)
	if code != 1 {
		t.Fatalf("undetected directory: code = %d", code)
	}
	if !strings.Contains(stderr.String(), "pf init") {
		t.Fatalf("undetected directory stderr = %q", stderr.String())
	}

	stderr.Reset()
	scriptPath := filepath.Join(t.TempDir(), "script.sh")
	writeProjectTestFile(t, scriptPath, "#!/bin/sh\necho hi\n", 0o755)
	code = suggestProjectConfig("build", scriptPath, &stderr)
	if code != 1 {
		t.Fatalf("detected script: code = %d", code)
	}
	out := stderr.String()
	if !strings.Contains(out, "detected a script project") || !strings.Contains(out, "pf build") {
		t.Fatalf("detected script stderr = %q", out)
	}
}

// TestSuggestProjectConfigDetectsAmbiguousLanguagePluginsForADirectory
// covers the real fix for the bug the previous dead-code sweep found:
// detect.Path alone can never set Ambiguous for a directory anymore
// (ecosystem classification moved to the language-plugin system - see
// suggestProjectConfig's own doc comment), so this branch used to be
// unreachable through this function even though internal/detect.Result
// still has the field and detectionFromPlugins (main.go's
// TestRunDetectSurfacesAmbiguousLanguagePlugins covers the same
// mechanism from `platform-factory detect` itself) can still set it. A
// directory carrying both go.mod and package.json matches the ambient
// go and node language plugins TestMain loads for this whole test
// binary simultaneously.
func TestSuggestProjectConfigDetectsAmbiguousLanguagePluginsForADirectory(t *testing.T) {
	root := t.TempDir()
	writeProjectTestFile(t, filepath.Join(root, "go.mod"), "module x\n", 0o644)
	writeProjectTestFile(t, filepath.Join(root, "package.json"), "{}", 0o644)
	var stderr bytes.Buffer
	code := suggestProjectConfig("run", root, &stderr)
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	out := stderr.String()
	if !strings.Contains(out, "detected multiple ecosystems") || !strings.Contains(out, "go") || !strings.Contains(out, "node") {
		t.Fatalf("stderr=%q", out)
	}
}

// TestSuggestProjectConfigDetectsASingleLanguagePluginMatchForADirectory
// is the non-ambiguous half of the same fix: a directory matching
// exactly one loaded language plugin now suggests `pf init` naming that
// language, instead of falling through to the generic "undetected
// directory" message every directory target used to get.
func TestSuggestProjectConfigDetectsASingleLanguagePluginMatchForADirectory(t *testing.T) {
	root := t.TempDir()
	writeProjectTestFile(t, filepath.Join(root, "go.mod"), "module x\n", 0o644)
	var stderr bytes.Buffer
	code := suggestProjectConfig("build", root, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	out := stderr.String()
	if !strings.Contains(out, "detected a go project") {
		t.Fatalf("stderr=%q", out)
	}
}

func TestWrapperCommand(t *testing.T) {
	got := wrapperCommand("dotnet")
	if runtime.GOOS == "windows" {
		if got != "dotnet.bat" {
			t.Fatalf("windows: wrapperCommand = %q", got)
		}
		return
	}
	if got != "dotnet" {
		t.Fatalf("non-windows: wrapperCommand = %q", got)
	}
}

// TestRunProjectContextRejectsMalformedFlags proves a genuine
// flag.Parse failure (a flag missing its required value) is handled
// distinctly from flag.ErrHelp - the surrounding switch only special-
// cases the latter.
func TestRunProjectContextRejectsMalformedFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"show", "--config"}, &stdout, &stderr, nil, nil, nil)
	if code != 2 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

// TestRunProjectBuildRejectsNegativeCPUBudgetWithoutAByteLimitError
// exercises the resource-budget validation's other error source: a
// negative duration flag with a perfectly valid --max-memory, so
// budgetErr itself is nil and the code has to synthesize its own
// "time budgets cannot be negative" error.
func TestRunProjectBuildRejectsNegativeCPUBudgetWithoutAByteLimitError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"build", "--dry-run", "--max-cpu=-1s", t.TempDir()}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil)
	if code != 2 || !strings.Contains(stderr.String(), "time budgets cannot be negative") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

// TestRunProjectBuildBlocksOnUnresolvedDependencyManagementBeforeAnyWork
// proves the dependency-state gate in runProjectContext itself (not
// buildProjectContextWithBudget, which TestProjectBuildRejectsChangedFrozenInputsBeforeRunningBuildCommand
// exercises directly) stops an unresolved/unknown dependency state
// before starting plugins or running any command.
func TestRunProjectBuildBlocksOnUnresolvedDependencyManagementBeforeAnyWork(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\ndependency_management:\n  mode: unresolved\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error {
		t.Fatal("build command executed despite an unresolved dependency state")
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"build", "--config", loaded.File}, &stdout, &stderr, execute, nil, nil)
	if code != 2 || !strings.Contains(stderr.String(), "dependency state is unresolved") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestRunProjectRunWatchStopsWhenContainerExitsOnItsOwn exercises the
// `run --watch` dispatch branch in runProjectContext together with
// runConfiguredProjectWatch's own "the container ended on its own"
// select case (as opposed to a rebuild-triggered stop, which
// TestRunConfiguredProjectWatchRebuildsOnChangeAndStopsOnCancel already
// covers) and the watch loop's own none-to-bridge network upgrade for
// published ports.
func TestRunProjectRunWatchStopsWhenContainerExitsOnItsOwn(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\nports: [9191:91]\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	pointBothRuntimeSocketsAtFakeEngine(t)
	var runArgs []string
	containerExecute := func(_ string, args []string, _ io.Reader, _, _ io.Writer) error {
		if len(args) > 0 && args[0] == "run" {
			runArgs = append([]string(nil), args...)
		}
		// Returning immediately (rather than blocking until a "stop"
		// call, as watchContainerExecuteStub does) simulates the
		// container crashing or running to completion on its own.
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"run", "--watch", "--config", loaded.File}, &stdout, &stderr, execute, containerExecute, nil)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	joined := strings.Join(runArgs, " ")
	if !strings.Contains(joined, "--network=bridge") || !strings.Contains(joined, "--publish=9191:91") {
		t.Fatalf("run args=%v", runArgs)
	}
}

// TestProjectLaunchWatchDispatchesToWatchLoop exercises launch's own
// --watch branch (distinct from run's - each has its own flag-guarded
// dispatch in runProjectContext) after launch's freeze-if-missing step.
func TestProjectLaunchWatchDispatchesToWatchLoop(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	pointBothRuntimeSocketsAtFakeEngine(t)
	ranContainer := false
	containerExecute := func(_ string, args []string, _ io.Reader, _, _ io.Writer) error {
		if len(args) > 0 && args[0] == "run" {
			ranContainer = true
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"launch", "--watch", "--config", loaded.File}, &stdout, &stderr, execute, containerExecute, nil)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !ranContainer {
		t.Fatal("expected launch --watch to run the container at least once")
	}
	if _, err := os.Stat(filepath.Join(loaded.Root, ".platform-factory", "freeze.lock.json")); err != nil {
		t.Fatalf("expected launch --watch to freeze first: %v", err)
	}
}

// TestProjectLaunchAcceptsRuntimeOverrideAndRejectsInvalid mirrors
// TestRunProjectRunAcceptsARuntimeOverride for the "launch" action,
// which has its own separate --runtime validation branch.
func TestProjectLaunchAcceptsRuntimeOverrideAndRejectsInvalid(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\nruntime_engine: podman\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	pointBothRuntimeSocketsAtFakeEngine(t)
	var runtimesSeen []string
	containerExecute := func(name string, args []string, _ io.Reader, _, _ io.Writer) error {
		if len(args) > 0 && args[0] == "run" {
			runtimesSeen = append(runtimesSeen, name)
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := runProject([]string{"launch", "--config", loaded.File, "--runtime", "docker"}, &stdout, &stderr, execute, containerExecute, nil); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, name := range runtimesSeen {
		if name != "docker" {
			t.Fatalf("runtimesSeen=%v, want every call to use docker", runtimesSeen)
		}
	}
	if code := runProject([]string{"launch", "--config", loaded.File, "--runtime", "bogus"}, &stdout, &stderr, execute, containerExecute, nil); code != 2 {
		t.Fatalf("expected an invalid --runtime value to be rejected, code=%d stderr=%s", code, stderr.String())
	}
}

// TestResolveFreezeStepsSurfacesGenericPluginFreezeError proves that a
// plugin-side freeze error unrelated to "no plugin provides a freeze
// adapter" (here: the plugin declares the capability but returns an
// invalid step) surfaces unchanged, rather than being folded into the
// built-in "no built-in freeze adapter" message the way errNoPluginFreeze
// is.
func TestResolveFreezeStepsSurfacesGenericPluginFreezeError(t *testing.T) {
	loaded := loadProjectTest(t, "language: zig\nartifact: app\n")
	host := &pluginHost{clients: []pluginClient{&stubPlugin{
		hello:  plugin.HelloResult{Name: "broken-adapter", Capabilities: []string{"freeze"}},
		freeze: api.FreezeResult{Steps: [][]string{{}}},
	}}}
	_, err := resolveFreezeSteps(loaded, host)
	if err == nil {
		t.Fatal("expected the plugin's own freeze error")
	}
	if strings.Contains(err.Error(), "no built-in freeze adapter") {
		t.Fatalf("err=%v, want the plugin's own error to surface unchanged", err)
	}
}

// TestExplainProjectActionSurfacesBuildCapabilityFailure exercises the
// dry-run build's own capability-preflight branch, distinct from the
// freeze-resolution failure TestExplainProjectActionSurfacesFreezeResolutionError
// already covers.
func TestExplainProjectActionSurfacesBuildCapabilityFailure(t *testing.T) {
	loaded := loadProjectTest(t, "language: python\nartifact: app\nprofile: python\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"build", "--dry-run", "--config", loaded.File}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil)
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"valid": false`) || !strings.Contains(stdout.String(), "does not fetch or build") {
		t.Fatalf("stdout=%s", stdout.String())
	}
}

// TestBuildProjectSurfacesCapabilityPreflightFailure exercises the same
// ValidateBuildCapability failure as
// TestExplainProjectActionSurfacesBuildCapabilityFailure, but through a
// real (non-dry-run) build.
func TestBuildProjectSurfacesCapabilityPreflightFailure(t *testing.T) {
	loaded := loadProjectTest(t, "language: python\nartifact: app\nprofile: python\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	if _, code := buildProject(loaded, &stdout, &stderr, execute); code != 2 || !strings.Contains(stderr.String(), "capability preflight failed") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestBuildProjectSurfacesImageFilesErrorWhenFreezeIsNotRequired
// exercises loaded.ImageFiles()'s own direct error branch inside
// buildProjectContextWithBudget - reached only when RequiresFrozenInputs
// is false (so VerifyFreezeInventory's own, earlier ImageFiles call
// never runs). A symlink under the project root, picked up by the
// implicit "." dependency every non-go/compiled/custom language adds,
// is rejected regardless of any freeze state.
func TestBuildProjectSurfacesImageFilesErrorWhenFreezeIsNotRequired(t *testing.T) {
	loaded := loadProjectTest(t, "language: rust\nartifact: app\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	if err := os.Symlink(filepath.Join(loaded.Root, "app"), filepath.Join(loaded.Root, "bad-link")); err != nil {
		t.Fatal(err)
	}
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	if _, code := buildProject(loaded, &stdout, &stderr, execute); code != 1 || !strings.Contains(stderr.String(), "dependencies:") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestBuildProjectSurfacesLanguagePluginResolutionFailure exercises
// buildProjectContextWithBudget's own error-forwarding around
// projectapp.LanguagePluginLayer, using the real production resolver
// (resolveLoadedPlugin) against a language with no loaded plugin.
func TestBuildProjectSurfacesLanguagePluginResolutionFailure(t *testing.T) {
	loaded := loadProjectTest(t, "language: cobol\nartifact: app\nlanguage_plugin: true\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	if _, code := buildProject(loaded, &stdout, &stderr, execute); code != 1 || !strings.Contains(stderr.String(), "isn't installed") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestBuildProjectIncludesLanguagePluginLayer is
// TestBuildProjectSurfacesLanguagePluginResolutionFailure's positive
// counterpart: language "python" is already loaded for this whole test
// binary (see TestMain), so resolveLoadedPlugin succeeds for real, and
// this test's own execute stub simulates the plugin's build-layer
// subcommand by writing a minimal valid tar to the requested --output
// path - proving the resulting layer is actually threaded into the
// build as an extra OCI layer.
func TestBuildProjectIncludesLanguagePluginLayer(t *testing.T) {
	// A bare "artifact: app" leaves the default entrypoint's basename as
	// "app", which oci.BuildConfig.Validate() then rejects for the
	// "python" profile ("python profile requires a matching runtime
	// entrypoint") - "runtime: python" + "args: [app]" is the same
	// working shape TestBuildProjectUsesRuntimeEntrypointDefault already
	// uses to get a real python-profile build past that check.
	loaded := loadProjectTest(t, "language: python\nruntime: python\nargs: [app]\nlanguage_plugin: true\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "python"), "binary", 0o755)
	execute := func(_ string, args []string, _ string, _, _ io.Writer) error {
		if len(args) == 0 || args[0] != "build-layer" {
			return nil
		}
		output := ""
		for index, argument := range args {
			if argument == "--output" && index+1 < len(args) {
				output = args[index+1]
			}
		}
		if output == "" {
			t.Fatal("build-layer invoked without --output")
		}
		file, err := os.Create(output)
		if err != nil {
			return err
		}
		defer file.Close()
		writer := tar.NewWriter(file)
		content := []byte("marker")
		if err := writer.WriteHeader(&tar.Header{
			Name: "app/.platform-factory/deps/python/marker", Typeflag: tar.TypeReg,
			Mode: 0o644, Size: int64(len(content)),
		}); err != nil {
			return err
		}
		if _, err := writer.Write(content); err != nil {
			return err
		}
		return writer.Close()
	}
	var stdout, stderr bytes.Buffer
	digest, code := buildProject(loaded, &stdout, &stderr, execute)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if digest == "" {
		t.Fatal("expected a digest")
	}
	if _, err := layout.Verify(loaded.Output()); err != nil {
		t.Fatal(err)
	}
}

// TestBuildProjectFallsBackToNowWhenNoProjectFileHasABirthTime exercises
// earliestProjectFileTime's zero-value fallback: the project's config
// file lives outside its own Root (via an explicit `project:` override)
// and the only file under Root sits inside a skipped directory ("dist"),
// so the walk finds no regular file to time at all.
func TestBuildProjectFallsBackToNowWhenNoProjectFileHasABirthTime(t *testing.T) {
	configDir := t.TempDir()
	root := t.TempDir()
	writeProjectTestFile(t, filepath.Join(configDir, ".config_image.yaml"),
		"language: compiled\nartifact: dist/app\nproject: "+root+"\noutput: build-out\n", 0o644)
	writeProjectTestFile(t, filepath.Join(root, "dist", "app"), "binary", 0o755)
	loaded, err := project.Load(filepath.Join(configDir, ".config_image.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Root != root {
		t.Fatalf("root=%q, want %q (test fixture assumption broken)", loaded.Root, root)
	}
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	if _, code := buildProject(loaded, &stdout, &stderr, execute); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := layout.Verify(loaded.Output()); err != nil {
		t.Fatal(err)
	}
}

// TestBuildProjectSurfacesReleaseSBOMWriteFailure, ...ReportWriteFailure,
// ...EvidenceWriteFailure and ...MetricsWriteFailure each block exactly
// one of the four late-stage release writes buildProjectContextWithBudget
// performs after a successful oci.Build, in pipeline order - each test
// leaves every earlier write free to succeed so only its own targeted
// write fails. output: build-out keeps the real build artifact outside
// .platform-factory entirely, so none of these collisions ever affect
// oci.Build itself.
func TestBuildProjectSurfacesReleaseSBOMWriteFailure(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\noutput: build-out\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	// .platform-factory exists as a plain file, not a directory, so
	// WriteSBOMToDist's own MkdirAll(releaseDir) fails first.
	writeProjectTestFile(t, filepath.Join(loaded.Root, ".platform-factory"), "not a directory", 0o644)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	if _, code := buildProject(loaded, &stdout, &stderr, execute); code != 1 || !strings.Contains(stderr.String(), "write release SBOM") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestBuildProjectSurfacesBuildReportWriteFailure(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\noutput: build-out\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	releaseDir := filepath.Join(loaded.Root, ".platform-factory", "release")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// "reports" already exists as a plain file, not a directory, so the
	// build report's own MkdirAll(reportsDir) fails - sbom.json (written
	// directly into releaseDir, above) already succeeded.
	writeProjectTestFile(t, filepath.Join(releaseDir, "reports"), "not a directory", 0o644)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	if _, code := buildProject(loaded, &stdout, &stderr, execute); code != 1 || !strings.Contains(stderr.String(), "write build report") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestBuildProjectSurfacesReleaseEvidenceWriteFailure(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\noutput: build-out\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	releaseDir := filepath.Join(loaded.Root, ".platform-factory", "release")
	reportsDir := filepath.Join(releaseDir, "reports")
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// provenance.json already exists as a directory, not a file, so
	// WriteBuildEvidence's own atomic rename onto it fails with EISDIR -
	// sbom.json and build.json already succeeded.
	if err := os.MkdirAll(filepath.Join(releaseDir, "provenance.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	if _, code := buildProject(loaded, &stdout, &stderr, execute); code != 1 || !strings.Contains(stderr.String(), "write release evidence") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestBuildProjectSurfacesMetricsWriteFailure(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\noutput: build-out\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	reportsDir := filepath.Join(loaded.Root, ".platform-factory", "release", "reports")
	// metrics.json already exists as a directory, not a file, so only the
	// very last write (after build.json, provenance.json and
	// layout.Verify have all already succeeded) fails with EISDIR.
	if err := os.MkdirAll(filepath.Join(reportsDir, "metrics.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	if _, code := buildProject(loaded, &stdout, &stderr, execute); code != 1 || !strings.Contains(stderr.String(), "write metrics") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestRebuildProjectLayoutSurfacesAutomaticFreezeFailure exercises
// rebuildProjectLayout's own automatic re-freeze: an explicit include
// makes RequiresFrozenInputs true, no freeze.lock.json exists yet, and
// "cobol" has no built-in freeze adapter, so the automatic freeze itself
// fails and rebuildProjectLayout must surface that failure without ever
// reaching os.RemoveAll or a build.
func TestRebuildProjectLayoutSurfacesAutomaticFreezeFailure(t *testing.T) {
	loaded := loadProjectTest(t, "language: cobol\nartifact: app\ninclude:\n  - {source: app, destination: /app/app}\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	code := rebuildProjectLayout(context.Background(), loaded, nil, &stdout, &stderr, execute)
	if code != 2 || !strings.Contains(stderr.String(), "no built-in freeze adapter") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(loaded.Output()); !os.IsNotExist(err) {
		t.Fatalf("expected no layout to be produced: %v", err)
	}
}

// TestRebuildProjectLayoutSurfacesStaleOutputRemovalFailure exercises
// rebuildProjectLayout's os.RemoveAll error branch: the stale output
// directory's parent is read-only, so RemoveAll can list its "image"
// entry but cannot unlink it.
func TestRebuildProjectLayoutSurfacesStaleOutputRemovalFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	writeProjectTestFile(t, filepath.Join(loaded.Output(), "marker"), "stale", 0o644)
	parent := filepath.Dir(loaded.Output())
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	code := rebuildProjectLayout(context.Background(), loaded, nil, &stdout, &stderr, execute)
	if code != 1 || !strings.Contains(stderr.String(), "remove stale output") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestRunConfiguredProjectSurfacesNeedsRebuildStatError and its watch
// counterpart exercise NeedsRebuild's other error path: an os.Stat
// failure that is not ENOENT (here, ENOTDIR - loaded.Output() itself is
// a plain file, so stat'ing index.json inside it cannot succeed or
// report a clean "missing").
func TestRunConfiguredProjectSurfacesNeedsRebuildStatError(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	writeProjectTestFile(t, loaded.Output(), "not a directory", 0o644)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	code := runConfiguredProject(context.Background(), loaded, nil, &stdout, &stderr, execute, nil, nil)
	if code != 1 || !strings.Contains(stderr.String(), "stat image") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunConfiguredProjectWatchSurfacesNeedsRebuildStatError(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	writeProjectTestFile(t, loaded.Output(), "not a directory", 0o644)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	containerExecute := func(string, []string, io.Reader, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	code := runConfiguredProjectWatch(context.Background(), loaded, nil, &stdout, &stderr, execute, containerExecute, time.Second)
	if code != 1 || !strings.Contains(stderr.String(), "stat image") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestRunConfiguredProjectWatchRejectsMicroVMIsolation exercises the
// watch loop's own microvm guard - it returns before ever registering
// the stopWatchedContainer defer, so passing nil executors is safe.
func TestRunConfiguredProjectWatchRejectsMicroVMIsolation(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\nisolation: microvm\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	var stdout, stderr bytes.Buffer
	code := runConfiguredProjectWatch(context.Background(), loaded, nil, &stdout, &stderr, nil, nil, time.Second)
	if code != 2 || !strings.Contains(stderr.String(), "does not support microvm isolation") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestRunConfiguredProjectWatchSurfacesRebuildFailure exercises the
// watch loop's own propagation of a failing rebuildProjectLayout call
// (here: the configured build_command itself fails), before any
// container is ever started.
func TestRunConfiguredProjectWatchSurfacesRebuildFailure(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\nbuild_command: [tool, build]\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	execute := func(_ string, args []string, _ string, _, _ io.Writer) error {
		if len(args) > 0 && args[0] == "build" {
			return errors.New("build failed")
		}
		return nil
	}
	containerExecute := func(string, []string, io.Reader, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	code := runConfiguredProjectWatch(context.Background(), loaded, nil, &stdout, &stderr, execute, containerExecute, time.Second)
	if code != 1 || !strings.Contains(stderr.String(), "command failed") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestRunConfiguredProjectWatchReturnsImmediatelyWhenContextAlreadyCanceled
// exercises the watch loop's ctx.Err() check between a (skipped)
// rebuild and starting a container: with a fresh, already-current
// layout (rebuild=false) and an already-canceled context, the loop must
// return before ever running a container.
func TestRunConfiguredProjectWatchReturnsImmediatelyWhenContextAlreadyCanceled(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	if _, code := buildProject(loaded, io.Discard, io.Discard, execute); code != 0 {
		t.Fatalf("prebuild code=%d", code)
	}
	ranContainer := false
	containerExecute := func(_ string, args []string, _ io.Reader, _, _ io.Writer) error {
		if len(args) > 0 && args[0] == "run" {
			ranContainer = true
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	code := runConfiguredProjectWatch(ctx, loaded, nil, &stdout, &stderr, execute, containerExecute, time.Second)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if ranContainer {
		t.Fatal("expected the watch loop to exit before starting a container when ctx is already canceled")
	}
}

// TestFreezeStepsHonorsDependencyManagementModes exercises every
// dependency_management.mode branch freezeSteps itself checks before
// ever consulting the language - "none"/"external" skip freezing
// entirely, "unresolved"/"unknown" refuse to freeze at all.
func TestFreezeStepsHonorsDependencyManagementModes(t *testing.T) {
	none := loadProjectTest(t, "language: go\nartifact: app\ndependency_management:\n  mode: none\n")
	if steps, err := freezeSteps(none); err != nil || steps != nil {
		t.Fatalf("mode=none steps=%v err=%v", steps, err)
	}
	external := loadProjectTest(t, "language: go\nartifact: app\ndependency_management:\n  mode: external\n")
	if steps, err := freezeSteps(external); err != nil || steps != nil {
		t.Fatalf("mode=external steps=%v err=%v", steps, err)
	}
	unresolved := loadProjectTest(t, "language: go\nartifact: app\ndependency_management:\n  mode: unresolved\n")
	if _, err := freezeSteps(unresolved); err == nil || !strings.Contains(err.Error(), "dependency state is unresolved") {
		t.Fatalf("mode=unresolved err=%v", err)
	}
	unknown := loadProjectTest(t, "language: go\nartifact: app\ndependency_management:\n  mode: unknown\n")
	if _, err := freezeSteps(unknown); err == nil || !strings.Contains(err.Error(), "dependency state is unknown") {
		t.Fatalf("mode=unknown err=%v", err)
	}
}

// TestMigrateProjectSurfacesValidationFailureOnMigratedDocument
// exercises migrateProject's own post-migration validation: this
// document migrates cleanly (no "version" field just means the v0->v1
// step stamps version: 1), but the migrated result is still missing
// "language", so loading it back to validate fails - distinct from
// every other migrate failure mode already covered, which all fail
// before ever producing a document to validate.
func TestMigrateProjectSurfacesValidationFailureOnMigratedDocument(t *testing.T) {
	root := t.TempDir()
	filename := filepath.Join(root, ".config_image.yaml")
	writeProjectTestFile(t, filename, "artifact: app\n", 0o644)
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"migrate", "--config", filename}, &stdout, &stderr, nil, nil, nil)
	if code != 1 || !strings.Contains(stderr.String(), "migrated config does not validate") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".migrate-") {
			t.Fatalf("temporary migration file left behind: %s", entry.Name())
		}
	}
}

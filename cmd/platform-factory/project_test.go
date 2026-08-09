package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/layout"
	"github.com/CYPT71/secure-oci-base/internal/project"
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
	if code := runProject([]string{"build", "--dry-run", "--config", loaded.File}, &stdout, &stderr, execute, nil, nil); code != 0 {
		t.Fatalf("build dry-run code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "make") {
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

func TestRunProjectSuggestsConfigFromDetection(t *testing.T) {
	root := t.TempDir()
	writeProjectTestFile(t, filepath.Join(root, "go.mod"), "module example.test/app\n", 0o644)
	var stdout, stderr bytes.Buffer
	code := runProject([]string{"show", root}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil)
	if code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"detected a go project", "language: go", ".config_image.yaml"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr missing %q: %s", want, stderr.String())
		}
	}
	ambiguous := t.TempDir()
	writeProjectTestFile(t, filepath.Join(ambiguous, "go.mod"), "module example.test/app\n", 0o644)
	writeProjectTestFile(t, filepath.Join(ambiguous, "package-lock.json"), "{}", 0o644)
	stderr.Reset()
	code = runProject([]string{"show", ambiguous}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil)
	if code != 2 || !strings.Contains(stderr.String(), "go, node") {
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

func TestProjectPlanReportsDetectionMismatch(t *testing.T) {
	loaded := loadProjectTest(t, "language: compiled\nartifact: app\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	writeProjectTestFile(t, filepath.Join(loaded.Root, "go.mod"), "module example.test/app\n", 0o644)
	var stdout, stderr bytes.Buffer
	if code := planProject(loaded, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{`"detected"`, `"kind": "go"`, `"matches_language": false`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q: %s", want, stdout.String())
		}
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
	if len(calls) != 2 || calls[0][0] != "image" {
		t.Fatalf("calls=%v", calls)
	}
	joined := strings.Join(calls[1], " ")
	for _, want := range []string{"--network=bridge", "--publish=8080:80", "--publish=8443:443"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %s", want, joined)
		}
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
		if got := projectProfile(language); got != want {
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

func TestRunProjectRejectsInvalidActionsAndMissingConfig(t *testing.T) {
	execute := func(string, []string, string, io.Writer, io.Writer) error { return nil }
	for _, args := range [][]string{nil, {"unknown"}, {"show", t.TempDir()}} {
		var stdout, stderr bytes.Buffer
		if code := runProject(args, &stdout, &stderr, execute, nil, nil); code == 0 {
			t.Fatalf("args=%v unexpectedly succeeded", args)
		}
	}
}

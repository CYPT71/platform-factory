package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/project"
)

func writeInitSourceArchive(t *testing.T, filename string, compressed bool) {
	t.Helper()
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	var writer io.Writer = file
	var gz *gzip.Writer
	if compressed {
		gz = gzip.NewWriter(file)
		writer = gz
	}
	tw := tar.NewWriter(writer)
	content := []byte("print('archive')\n")
	_ = tw.WriteHeader(&tar.Header{Name: "app.py", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))})
	_, _ = tw.Write(content)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if gz != nil {
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRunInitExtractsTarSourcesTransactionally(t *testing.T) {
	for _, format := range []string{"tar", "tar.gz"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			archive := filepath.Join(root, "source."+format)
			writeInitSourceArchive(t, archive, format == "tar.gz")
			destination := filepath.Join(root, "project")
			var stdout, stderr bytes.Buffer
			args := []string{"--extract-to", destination, "--archive-format", format, "--language", "python", "--runtime", "container", "--yes", archive}
			if code := runInit(args, nil, &stdout, &stderr); code != 0 {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
			for _, name := range []string{"app.py", "pf.yaml", "pf.lock", ".pf/inventory.json", ".pf/build.pipeline.json"} {
				if _, err := os.Stat(filepath.Join(destination, name)); err != nil {
					t.Fatalf("missing %s: %v", name, err)
				}
			}
		})
	}
}

func TestRunInitArchiveDryRunAndFailureLeaveNoDestination(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "source.tar")
	writeInitSourceArchive(t, archive, false)
	for name, args := range map[string][]string{
		"dry run":          {"--dry-run", "--extract-to", filepath.Join(root, "preview"), "--archive-format", "tar", "--language", "python", archive},
		"unknown language": {"--extract-to", filepath.Join(root, "failure"), "--archive-format", "tar", "--language", "missing", "--yes", archive},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runInit(args, nil, &stdout, &stderr)
			if name == "dry run" && code != 0 {
				t.Fatalf("dry-run code=%d stderr=%s", code, stderr.String())
			}
			if name != "dry run" && code == 0 {
				t.Fatal("expected init failure")
			}
			destination := args[2]
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("destination remains: %v", err)
			}
		})
	}
}

func TestRunInitDetectsEcosystemAndWritesLoadableConfig(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n", 0o644)
	writeGoInitSource(t, dir)

	var stdout, stderr bytes.Buffer
	if code := runInit([]string{dir}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}

	configPath := filepath.Join(dir, "pf.yaml")
	loaded, err := project.Load(configPath)
	if err != nil {
		t.Fatalf("generated config does not load: %v", err)
	}
	if loaded.Config.Language != "go" {
		t.Fatalf("language=%q, want go", loaded.Config.Language)
	}
	if loaded.Config.Artifact == "" {
		t.Fatal("artifact must be non-empty (a placeholder), Validate requires it")
	}
	if _, err := os.Stat(filepath.Join(loaded.Root, "pf.lock")); err != nil {
		t.Fatalf("pf.lock missing: %v", err)
	}
}

func TestRunInitRejectsUnknownLocalEngineBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := runInit([]string{"--engine", "containerd", dir}, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--engine must be docker or podman") {
		t.Fatalf("stderr=%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "pf.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("init wrote config: %v", err)
	}
}

func TestRunInitAmbiguousEcosystemNonInteractiveFailsClosedWithoutWritingJunk(t *testing.T) {
	// Without a terminal (or --yes/--language) to ask, pf init must not
	// invent a fake "REPLACE_ME" language just to produce a
	// superficially-loadable config - that config would only fail later,
	// confusingly, deep inside `project build`/`freeze`. It should
	// refuse up front instead, and write nothing at all.
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n", 0o644)
	writeProjectTestFile(t, filepath.Join(dir, "package-lock.json"), "{}", 0o644)

	var stdout, stderr bytes.Buffer
	code := runInit([]string{dir}, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "pf plugin load") {
		t.Fatalf("stderr=%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "pf.yaml")); err == nil {
		t.Fatal("expected no config to be written when the ecosystem is ambiguous and nothing can resolve it")
	}
}

func TestRunInitExternalImportsRequireExplicitDependencyDecision(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "app.py"), "import requests\n", 0o644)
	var stdout, stderr bytes.Buffer
	if code := runInit([]string{dir}, nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "--dependency-mode") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "pf.yaml")); !os.IsNotExist(err) {
		t.Fatalf("pf.yaml written before decision: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runInit([]string{"--dependency-mode=unresolved", dir}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(dir, "pf.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "mode: unresolved") {
		t.Fatalf("config=%s", raw)
	}
}

func TestRunInitAmbiguousEcosystemResolvedByLanguageFlag(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n", 0o644)
	writeProjectTestFile(t, filepath.Join(dir, "package-lock.json"), "{}", 0o644)

	var stdout, stderr bytes.Buffer
	code := runInit([]string{"--yes", "--language=go", "--artifact=cmd/app/main.go", dir}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	loaded, err := project.Load(filepath.Join(dir, "pf.yaml"))
	if err != nil {
		t.Fatalf("generated config does not load: %v", err)
	}
	if loaded.Config.Language != "go" || loaded.Config.Artifact != "cmd/app/main.go" {
		t.Fatalf("Language=%q Artifact=%q", loaded.Config.Language, loaded.Config.Artifact)
	}
}

func TestRunInitMigratesLegacyConfigAndRemovesOldFile(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, ".config_image.yaml")
	writeProjectTestFile(t, legacy, "version: 1\nlanguage: python\nartifact: app.py\n", 0o644)

	var stdout, stderr bytes.Buffer
	if code := runInit([]string{dir}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy config was not removed: err=%v", err)
	}
	loaded, err := project.Load(filepath.Join(dir, "pf.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.Language != "python" || loaded.Config.Artifact != "app.py" {
		t.Fatalf("migrated config=%+v, want language=python artifact=app.py", loaded.Config)
	}
	if !strings.Contains(stdout.String(), "migrated from") {
		t.Fatalf("migration not reported: stdout=%s", stdout.String())
	}
	trace, err := os.ReadFile(filepath.Join(dir, ".pf", "migration.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"api_version": "platform-factory.dev/project-migration/v1"`, `"from": ".config_image.yaml"`, `"to": "pf.yaml"`, `"normalized": false`, `"field": "filename"`, `"reason": "use the canonical project configuration filename; document bytes and values are unchanged"`} {
		if !strings.Contains(string(trace), want) {
			t.Fatalf("migration trace missing %q: %s", want, trace)
		}
	}
}

func TestRunInitFailsClosedWhenAlreadyInitialized(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "platform-factory.yaml"), "version: 1\nlanguage: go\nartifact: app\n", 0o644)

	var stdout, stderr bytes.Buffer
	code := runInit([]string{dir}, nil, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "already initialized") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".pf")); !os.IsNotExist(err) {
		t.Fatal("init wrote scaffolding despite already being initialized")
	}
}

func TestRunInitSkipsAnAlreadyExistingGitRepo(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n", 0o644)
	writeGoInitSource(t, dir)
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	sentinel := filepath.Join(dir, ".git", "sentinel")
	writeProjectTestFile(t, sentinel, "pre-existing repo marker\n", 0o644)

	var stdout, stderr bytes.Buffer
	if code := runInit([]string{dir}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("existing .git was touched/reinitialized, sentinel gone: %v", err)
	}
	if strings.Contains(stdout.String(), "git repository") {
		t.Fatalf("init claimed to create a git repo that already existed: stdout=%s", stdout.String())
	}
}

func TestRunInitFromNestedGitDirectoryInitializesRepositoryRoot(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n", 0o644)
	writeGoInitSource(t, dir)
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	nested := filepath.Join(dir, "cmd", "demo")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runInit([]string{"--yes", "--language=go", "--artifact=app", nested}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "pf.yaml")); err != nil {
		t.Fatalf("repository root was not initialized: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nested, "pf.yaml")); !os.IsNotExist(err) {
		t.Fatalf("nested source was incorrectly initialized: %v", err)
	}
}

func TestRunInitPreservesCustomBuildCommandArgumentBoundaries(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "app.py"), "print('ok')\n", 0o644)
	var stdout, stderr bytes.Buffer
	args := []string{"--yes", "--language=python", "--artifact=app.py", "--build-command=builder tool", "--build-arg=arg with spaces", "--build-arg=$HOME;touch nope", dir}
	if code := runInit(args, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	loaded, err := project.Load(filepath.Join(dir, "pf.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"builder tool", "arg with spaces", "$HOME;touch nope"}
	if !slices.Equal(loaded.Config.BuildCommand, want) {
		t.Fatalf("build command=%q want=%q", loaded.Config.BuildCommand, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "nope")); !os.IsNotExist(err) {
		t.Fatalf("shell metacharacters were interpreted: %v", err)
	}
}

func TestRunInitRejectsBuildArgsWithoutExecutable(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := runInit([]string{"--build-arg=x", dir}, nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "requires --build-command") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("invalid command mutated project: %v", entryNames(entries))
	}
}

func TestRunInitPreservesAnExistingGitignore(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n", 0o644)
	writeGoInitSource(t, dir)
	custom := "# my own ignores\n*.local\n"
	writeProjectTestFile(t, filepath.Join(dir, ".gitignore"), custom, 0o644)

	var stdout, stderr bytes.Buffer
	if code := runInit([]string{dir}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != custom {
		t.Fatalf(".gitignore was overwritten: got=%q want=%q", got, custom)
	}
}

func TestRunInitDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n", 0o644)
	writeGoInitSource(t, dir)
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := runInit([]string{"--dry-run", dir}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "pf.yaml") {
		t.Fatalf("dry-run plan missing expected entries: stdout=%s", stdout.String())
	}
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("--dry-run wrote to disk: before=%d entries after=%d entries", len(before), len(after))
	}
}

func TestRunInitDryRunExplainsMultiComponentProposalWithoutSelectingRuntime(t *testing.T) {
	dir := t.TempDir()
	for component, marker := range map[string]string{"worker": "Cargo.toml", "api": "go.mod"} {
		componentDir := filepath.Join(dir, component)
		if err := os.Mkdir(componentDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(componentDir, marker), []byte("marker\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runInit([]string{"--dry-run", "--yes", "--language=go", dir}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	output := stdout.String()
	for _, expected := range []string{"language go", "recommended runtime container", "system proposal"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("dry-run missing %q:\n%s", expected, output)
		}
	}
	if strings.Contains(output, "component api") || strings.Contains(output, "component worker") {
		t.Fatalf("the language-neutral core invented components:\n%s", output)
	}
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("dry-run mutated project: before=%d after=%d", len(before), len(after))
	}
}

func TestRunInitCreatesCompleteSafeProjectScaffold(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n", 0o644)
	writeGoInitSource(t, dir)
	preexisting := filepath.Join(dir, "README.md")
	writeProjectTestFile(t, preexisting, "pre-existing user file\n", 0o644)
	var stdout, stderr bytes.Buffer
	code := runInit([]string{dir}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, name := range []string{"pf.yaml", "pf.lock", ".gitignore", ".git", ".pf", "policies", "deploy", "dist", "reports"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("required %s was not created: %v", name, err)
		}
	}
	for _, name := range []string{"platform-factory.yaml", "platform-factory.lock"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("non-canonical alias %s was unexpectedly created: err=%v", name, err)
		}
	}
	ignored, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil || !strings.Contains(string(ignored), "dist/") || !strings.Contains(string(ignored), ".platform-factory/") {
		t.Fatalf("generated .gitignore is incomplete: content=%q err=%v", ignored, err)
	}
	got, err := os.ReadFile(preexisting)
	if err != nil || string(got) != "pre-existing user file\n" {
		t.Fatalf("pre-existing user file was not preserved: content=%q err=%v", got, err)
	}
}

func TestRunInitLongFilenameStyleCreatesDiscoverableLongPair(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/long\n\ngo 1.22\n", 0o644)
	writeGoInitSource(t, dir)
	var stdout, stderr bytes.Buffer
	if code := runInit([]string{"--yes", "--filename-style", "long", dir}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, name := range []string{"platform-factory.yaml", "platform-factory.lock"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	for _, name := range []string{"pf.yaml", "pf.lock"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected alias %s: %v", name, err)
		}
	}
	if _, err := project.Discover(dir, ""); err != nil {
		t.Fatalf("long project does not discover: %v", err)
	}
}

func TestRunInitRejectsInvalidFilenameStyleWithoutWrites(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := runInit([]string{"--filename-style", "both", dir}, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid style mutated project: %v %v", entries, err)
	}
}

func TestRunInitRejectsNonDirectorySource(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	writeProjectTestFile(t, file, "x", 0o644)

	var stdout, stderr bytes.Buffer
	if code := runInit([]string{file}, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runInit([]string{filepath.Join(dir, "does-not-exist")}, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func writeRawOSDiskFixture(t *testing.T, path string) {
	t.Helper()
	buf := make([]byte, 4096)
	buf[446] = 0x80 // MBR active/bootable flag
	buf[446+4] = 0x83
	buf[510], buf[511] = 0x55, 0xaa
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("write disk fixture %s: %v", path, err)
	}
}

func writeRawDataDiskFixture(t *testing.T, path string) {
	t.Helper()
	buf := make([]byte, 4096)
	buf[510], buf[511] = 0x55, 0xaa // valid MBR, no active partition
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("write disk fixture %s: %v", path, err)
	}
}

func TestRunInitDetectsSingleBootableLegacyDiskAutomatically(t *testing.T) {
	dir := t.TempDir()
	writeRawOSDiskFixture(t, filepath.Join(dir, "disk.raw"))

	var stdout, stderr bytes.Buffer
	if code := runInit([]string{dir}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "found 1 legacy VM disk") {
		t.Fatalf("stdout=%s", stdout.String())
	}

	loaded, err := project.Load(filepath.Join(dir, "pf.yaml"))
	if err != nil {
		t.Fatalf("generated config does not load: %v", err)
	}
	if loaded.Config.LegacyDisks == nil || loaded.Config.LegacyDisks.Boot != "disk.raw" {
		t.Fatalf("LegacyDisks=%+v", loaded.Config.LegacyDisks)
	}
	if loaded.Config.LegacyDisks.Strategy != "unsupported" {
		t.Fatalf("Strategy=%q, want fail-closed unsupported", loaded.Config.LegacyDisks.Strategy)
	}
	// No application source exists here, only the disk - the config
	// must not carry a made-up language/artifact just to satisfy the
	// schema (see the LegacyDisks exemption in project.Validate).
	if loaded.Config.Language != "" || loaded.Config.Artifact != "" {
		t.Fatalf("Language=%q Artifact=%q, want both empty for a pure legacy-disk project", loaded.Config.Language, loaded.Config.Artifact)
	}
}

func TestRunInitRetainsExplicitLegacyStrategy(t *testing.T) {
	dir := t.TempDir()
	writeRawOSDiskFixture(t, filepath.Join(dir, "disk.raw"))

	var stdout, stderr bytes.Buffer
	if code := runInit([]string{"--legacy-strategy=vm-encapsulation", dir}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	loaded, err := project.Load(filepath.Join(dir, "pf.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.LegacyDisks == nil || loaded.Config.LegacyDisks.Strategy != "vm-encapsulation" {
		t.Fatalf("LegacyDisks=%+v", loaded.Config.LegacyDisks)
	}

	other := t.TempDir()
	var invalidErr bytes.Buffer
	if code := runInit([]string{"--legacy-strategy=guess", other}, nil, io.Discard, &invalidErr); code != 2 || !strings.Contains(invalidErr.String(), "--legacy-strategy") {
		t.Fatalf("invalid strategy code=%d stderr=%s", code, invalidErr.String())
	}
}

func TestRunInitLegacyDiskAmbiguousWithBootDiskOverride(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.raw")
	second := filepath.Join(dir, "second.raw")
	writeRawDataDiskFixture(t, first)
	writeRawDataDiskFixture(t, second)

	var stdout, stderr bytes.Buffer
	code := runInit([]string{"--boot-disk=" + second, dir}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	loaded, err := project.Load(filepath.Join(dir, "pf.yaml"))
	if err != nil {
		t.Fatalf("generated config does not load: %v", err)
	}
	if loaded.Config.LegacyDisks == nil || loaded.Config.LegacyDisks.Boot != "second.raw" {
		t.Fatalf("LegacyDisks=%+v", loaded.Config.LegacyDisks)
	}
	if len(loaded.Config.LegacyDisks.Data) != 1 || loaded.Config.LegacyDisks.Data[0] != "first.raw" {
		t.Fatalf("Data=%v", loaded.Config.LegacyDisks.Data)
	}
}

func TestRunInitLegacyDiskAmbiguousPromptsInteractively(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.raw")
	second := filepath.Join(dir, "second.raw")
	writeRawDataDiskFixture(t, first)
	writeRawDataDiskFixture(t, second)

	// Three prompts fire in this scenario, in order: boot-disk choice
	// (no application source exists, only disks, so ecosystem detection
	// is also unconfident), the ecosystem/language question, and the
	// final "proceed?" confirmation.
	stdin := strings.NewReader("1\ny\n")
	var stdout, stderr bytes.Buffer
	code := runInit([]string{dir}, stdin, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "enter the number of the boot disk") {
		t.Fatalf("stdout=%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Apply this plan?") {
		t.Fatalf("stdout=%s", stdout.String())
	}
	loaded, err := project.Load(filepath.Join(dir, "pf.yaml"))
	if err != nil {
		t.Fatalf("generated config does not load: %v", err)
	}
	// Disks are scanned in os.ReadDir order (alphabetical), so index 1 is second.raw.
	if loaded.Config.LegacyDisks == nil || loaded.Config.LegacyDisks.Boot != "second.raw" {
		t.Fatalf("LegacyDisks=%+v", loaded.Config.LegacyDisks)
	}
}

func TestRunInitLegacyDiskAmbiguousFailsClosedWithoutStdinOrOverride(t *testing.T) {
	dir := t.TempDir()
	writeRawDataDiskFixture(t, filepath.Join(dir, "first.raw"))
	writeRawDataDiskFixture(t, filepath.Join(dir, "second.raw"))

	var stdout, stderr bytes.Buffer
	code := runInit([]string{dir}, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--boot-disk") {
		t.Fatalf("stderr=%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "platform-factory.yaml")); err == nil {
		t.Fatal("expected no config to be written when the boot disk could not be resolved")
	}
}

func TestRunInitWithoutLegacyDisksOmitsLegacyDisksField(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n", 0o644)
	writeGoInitSource(t, dir)

	var stdout, stderr bytes.Buffer
	if code := runInit([]string{dir}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	loaded, err := project.Load(filepath.Join(dir, "pf.yaml"))
	if err != nil {
		t.Fatalf("generated config does not load: %v", err)
	}
	if loaded.Config.LegacyDisks != nil {
		t.Fatalf("LegacyDisks=%+v, want nil", loaded.Config.LegacyDisks)
	}
}

func TestRunInitAmbiguousEcosystemPromptsForLanguageAndArtifact(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n", 0o644)
	writeProjectTestFile(t, filepath.Join(dir, "package.json"), `{"name":"app"}`, 0o644)
	writeProjectTestFile(t, filepath.Join(dir, "package-lock.json"), `{}`, 0o644)

	stdin := strings.NewReader("1\ncmd/app/main.go\ny\n") // 1 = Go in the numbered menu
	var stdout, stderr bytes.Buffer
	code := runInit([]string{dir}, stdin, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "[1] go") {
		t.Fatalf("expected the numbered language menu, stdout=%s", stdout.String())
	}
	loaded, err := project.Load(filepath.Join(dir, "pf.yaml"))
	if err != nil {
		t.Fatalf("generated config does not load: %v", err)
	}
	if loaded.Config.Language != "go" || loaded.Config.Artifact != "cmd/app/main.go" {
		t.Fatalf("Language=%q Artifact=%q", loaded.Config.Language, loaded.Config.Artifact)
	}
}

func TestRunInitLanguageMenuRejectsOutOfRangeChoice(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n", 0o644)
	writeProjectTestFile(t, filepath.Join(dir, "package.json"), `{"name":"app"}`, 0o644)
	writeProjectTestFile(t, filepath.Join(dir, "package-lock.json"), `{}`, 0o644)

	stdin := strings.NewReader("99\n")
	var stdout, stderr bytes.Buffer
	code := runInit([]string{dir}, stdin, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"99" is not a valid choice`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "platform-factory.yaml")); err == nil {
		t.Fatal("expected no config to be written for an out-of-range menu choice")
	}
}

func TestRunInitDeclinedConfirmationWritesNothing(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n", 0o644)
	writeGoInitSource(t, dir)

	stdin := strings.NewReader("n\n")
	var stdout, stderr bytes.Buffer
	code := runInit([]string{dir}, stdin, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "aborted, nothing written") {
		t.Fatalf("stdout=%s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "platform-factory.yaml")); err == nil {
		t.Fatal("expected no config to be written after declining the confirmation prompt")
	}
}

func writeGoInitSource(t *testing.T, dir string) {
	t.Helper()
	writeProjectTestFile(t, filepath.Join(dir, "main.go"), "package main\nfunc main() {}\n", 0o644)
}

func TestRunInitAssumeYesSkipsAllPromptsEvenWithStdin(t *testing.T) {
	dir := t.TempDir()
	writeRawDataDiskFixture(t, filepath.Join(dir, "first.raw"))
	writeRawDataDiskFixture(t, filepath.Join(dir, "second.raw"))

	// --yes bypasses the boot-disk/ecosystem/confirm prompts entirely;
	// since neither disk has positive boot evidence, SelectBootDisk
	// itself still fails closed rather than --yes forcing a guess.
	stdin := strings.NewReader("")
	var stdout, stderr bytes.Buffer
	code := runInit([]string{"--yes", dir}, stdin, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "enter the number") {
		t.Fatalf("--yes should never prompt; stdout=%s", stdout.String())
	}
}

func isGitWorkTree(t *testing.T, dir string) bool {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").CombinedOutput()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

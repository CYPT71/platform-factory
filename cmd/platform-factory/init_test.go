package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/policy"
	"github.com/CYPT71/secure-oci-base/internal/project"
)

func TestRunInitDetectsEcosystemAndWritesLoadableConfig(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n", 0o644)

	var stdout, stderr bytes.Buffer
	if code := runInit([]string{dir}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}

	configPath := filepath.Join(dir, "platform-factory.yaml")
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
	for _, dir := range []string{".pf", "policies", "deploy", "dist", "reports"} {
		if info, err := os.Stat(filepath.Join(loaded.Root, dir)); err != nil || !info.IsDir() {
			t.Fatalf("%s directory missing: %v", dir, err)
		}
	}
	policyRules, err := os.ReadFile(filepath.Join(loaded.Root, "policies", "default.json"))
	if err != nil {
		t.Fatalf("policies/default.json missing: %v", err)
	}
	var rules policy.Rules
	if err := json.Unmarshal(policyRules, &rules); err != nil {
		t.Fatalf("policies/default.json does not decode as policy.Rules: %v", err)
	}
	if _, err := policy.Evaluate(rules, policy.Evidence{}); err != nil {
		t.Fatalf("policies/default.json does not Evaluate: %v", err)
	}
	for _, readme := range []string{
		filepath.Join("deploy", "README.md"),
		filepath.Join("reports", "README.md"),
		filepath.Join(".pf", "README.md"),
		filepath.Join("dist", "README.md"),
	} {
		if content, err := os.ReadFile(filepath.Join(loaded.Root, readme)); err != nil || len(content) == 0 {
			t.Fatalf("%s missing or empty: content=%q err=%v", readme, content, err)
		}
	}
	if _, err := os.Stat(filepath.Join(loaded.Root, ".gitignore")); err != nil {
		t.Fatalf(".gitignore missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(loaded.Root, "platform-factory.lock")); err != nil {
		t.Fatalf("platform-factory.lock missing: %v", err)
	}
	if !isGitWorkTree(t, loaded.Root) {
		t.Fatal("git init did not run")
	}
	if !strings.Contains(stdout.String(), "edit platform-factory.yaml") {
		t.Fatalf("missing placeholder-edit reminder: stdout=%s", stdout.String())
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
	if !strings.Contains(stderr.String(), "--language") {
		t.Fatalf("stderr=%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "platform-factory.yaml")); err == nil {
		t.Fatal("expected no config to be written when the ecosystem is ambiguous and nothing can resolve it")
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
	loaded, err := project.Load(filepath.Join(dir, "platform-factory.yaml"))
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
	loaded, err := project.Load(filepath.Join(dir, "platform-factory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.Language != "python" || loaded.Config.Artifact != "app.py" {
		t.Fatalf("migrated config=%+v, want language=python artifact=app.py", loaded.Config)
	}
	if !strings.Contains(stdout.String(), "migrated from") {
		t.Fatalf("migration not reported: stdout=%s", stdout.String())
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

func TestRunInitPreservesAnExistingGitignore(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n", 0o644)
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
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := runInit([]string{"--dry-run", dir}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "platform-factory.yaml") {
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
	for _, expected := range []string{
		"component api from api: recommended runtime container",
		"component worker from worker: recommended runtime container",
		"selected runtime unknown",
		"unknown connections",
		"unknown resources",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("dry-run missing %q:\n%s", expected, output)
		}
	}
	if strings.Index(output, "component api") > strings.Index(output, "component worker") {
		t.Fatalf("component explanation order is unstable:\n%s", output)
	}
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("dry-run mutated project: before=%d after=%d", len(before), len(after))
	}
}

// TestRunInitRollsBackEverythingOnMidPlanFailure forces the *last* plan
// step (git init) to fail - by making `git` unresolvable - so every
// earlier step (config, all five directories, .gitignore, the lock
// file) has genuinely already been created on disk before the failure,
// giving rollback real work to undo rather than nothing.
func TestRunInitRollsBackEverythingOnMidPlanFailure(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n", 0o644)
	preexisting := filepath.Join(dir, "README.md")
	writeProjectTestFile(t, preexisting, "pre-existing user file\n", 0o644)
	t.Setenv("PATH", t.TempDir()) // git becomes unresolvable

	var stdout, stderr bytes.Buffer
	code := runInit([]string{dir}, nil, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "rolled back") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, name := range []string{".pf", "policies", "deploy", "dist", "reports", "platform-factory.yaml", ".gitignore", "platform-factory.lock", ".git"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived a rolled-back init: err=%v", name, err)
		}
	}
	got, err := os.ReadFile(preexisting)
	if err != nil || string(got) != "pre-existing user file\n" {
		t.Fatalf("pre-existing user file was not preserved: content=%q err=%v", got, err)
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

	loaded, err := project.Load(filepath.Join(dir, "platform-factory.yaml"))
	if err != nil {
		t.Fatalf("generated config does not load: %v", err)
	}
	if loaded.Config.LegacyDisks == nil || loaded.Config.LegacyDisks.Boot != "disk.raw" {
		t.Fatalf("LegacyDisks=%+v", loaded.Config.LegacyDisks)
	}
	// No application source exists here, only the disk - the config
	// must not carry a made-up language/artifact just to satisfy the
	// schema (see the LegacyDisks exemption in project.Validate).
	if loaded.Config.Language != "" || loaded.Config.Artifact != "" {
		t.Fatalf("Language=%q Artifact=%q, want both empty for a pure legacy-disk project", loaded.Config.Language, loaded.Config.Artifact)
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
	loaded, err := project.Load(filepath.Join(dir, "platform-factory.yaml"))
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
	stdin := strings.NewReader("1\n\ny\n")
	var stdout, stderr bytes.Buffer
	code := runInit([]string{dir}, stdin, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "enter the number of the boot disk") {
		t.Fatalf("stdout=%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "proceed?") {
		t.Fatalf("stdout=%s", stdout.String())
	}
	loaded, err := project.Load(filepath.Join(dir, "platform-factory.yaml"))
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

	var stdout, stderr bytes.Buffer
	if code := runInit([]string{dir}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	loaded, err := project.Load(filepath.Join(dir, "platform-factory.yaml"))
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
	if !strings.Contains(stdout.String(), "1) Go") {
		t.Fatalf("expected the numbered language menu, stdout=%s", stdout.String())
	}
	loaded, err := project.Load(filepath.Join(dir, "platform-factory.yaml"))
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
	if !strings.Contains(stdout.String(), `"99" isn't one of the numbers above`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "platform-factory.yaml")); err == nil {
		t.Fatal("expected no config to be written for an out-of-range menu choice")
	}
}

func TestRunInitDeclinedConfirmationWritesNothing(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.22\n", 0o644)

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

package projectinit

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CYPT71/secure-oci-base/internal/detect"
	"github.com/CYPT71/secure-oci-base/internal/project"
)

func TestBuildPlanIsExplicitAndDoesNotMutate(t *testing.T) {
	dir := t.TempDir()
	plan, err := BuildPlan(dir, Ecosystem{Result: detect.Result{Kind: "go", Evidence: []string{"go.mod"}}, Artifact: "cmd/app/main.go", Confident: true}, nil, testObservations())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) == 0 || !strings.Contains(plan.Actions[0].Description(), "platform-factory.yaml") {
		t.Fatalf("plan does not explain its config mutation: %+v", plan.Actions)
	}
	if _, err := os.Stat(filepath.Join(dir, "platform-factory.yaml")); !os.IsNotExist(err) {
		t.Fatalf("planning mutated the project: %v", err)
	}
}

func TestExecuteRefusesFileAppearingAfterPlanning(t *testing.T) {
	dir := t.TempDir()
	plan, err := BuildPlan(dir, Ecosystem{Result: detect.Result{Kind: "go"}, Artifact: "app", Confident: true}, nil, testObservations())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "platform-factory.yaml")
	if err := os.WriteFile(target, []byte("do not overwrite"), 0o644); err != nil {
		t.Fatal(err)
	}
	receipt, err := Execute(plan)
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("expected exclusive-create failure, got %v", err)
	}
	if rollbackErr := Rollback(receipt); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "do not overwrite" {
		t.Fatalf("concurrent file changed: %q", got)
	}
}

func TestExecuteRefusesDirectoryAppearingAfterPlanning(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "new")
	plan := Plan{Actions: []Action{{kind: ActionMkdir, path: target}}}
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(plan); err == nil {
		t.Fatal("expected directory collision failure")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("concurrent directory removed: %v", err)
	}
}

func TestExecuteRefusesGitDirectoryAppearingAfterPlanning(t *testing.T) {
	dir := t.TempDir()
	plan := Plan{Actions: []Action{{kind: ActionGitInit, path: dir}}}
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	receipt, err := Execute(plan)
	if err == nil || !strings.Contains(err.Error(), "reserve git repository") {
		t.Fatalf("expected .git collision failure, got %v", err)
	}
	if err := Rollback(receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(gitDir); err != nil {
		t.Fatalf("concurrently created .git was not owned and must survive rollback: %v", err)
	}
}

func TestRollbackReportsRestoreCollision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.yaml")
	if err := os.WriteFile(path, []byte("concurrent"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Rollback(Receipt{migrations: []migrationBackup{{path: path, content: []byte("original"), mode: 0o640}}})
	if err == nil || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("expected observable rollback failure, got %v", err)
	}
}

func TestExecuteSuccessCreatesExclusivePlan(t *testing.T) {
	dir := t.TempDir()
	plan := Plan{Actions: []Action{
		{kind: ActionMkdir, path: filepath.Join(dir, "state")},
		{kind: ActionWriteFile, path: filepath.Join(dir, "state", "README.md"), content: []byte("state\n")},
	}}
	receipt, err := Execute(plan)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "state", "README.md")); err != nil || string(got) != "state\n" {
		t.Fatalf("created content=%q err=%v", got, err)
	}
	if err := Rollback(receipt); err != nil {
		t.Fatal(err)
	}
}

func TestBuildPlanRejectsSymlinkAndIsDeterministicForObservations(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "policies")); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPlan(dir, Ecosystem{}, nil, testObservations()); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "policies")); err != nil {
		t.Fatal(err)
	}
	eco := Ecosystem{Result: detect.Result{Kind: "go"}, Artifact: "app", Confident: true}
	first, err := BuildPlan(dir, eco, nil, testObservations())
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildPlan(dir, eco, nil, testObservations())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Actions) != len(second.Actions) {
		t.Fatalf("plan lengths differ: %d != %d", len(first.Actions), len(second.Actions))
	}
	for i := range first.Actions {
		if first.Actions[i].Description() != second.Actions[i].Description() || !bytes.Equal(first.Actions[i].content, second.Actions[i].content) {
			t.Fatalf("action %d is nondeterministic", i)
		}
	}
}

func TestBuildPlanPreservesUnknownArtifact(t *testing.T) {
	plan, err := BuildPlan(t.TempDir(), Ecosystem{Result: detect.Result{Kind: "go"}, Confident: true}, nil, testObservations())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Unknowns) != 1 || plan.Unknowns[0].Subject != "build.artifact" {
		t.Fatalf("unresolved artifact was not explicit: %+v", plan.Unknowns)
	}
}

func TestPlanHelpersExposeDecisions(t *testing.T) {
	dir := t.TempDir()
	plan := Plan{Actions: []Action{
		{kind: ActionWriteFile, path: filepath.Join(dir, "config"), placeholder: true},
		{kind: ActionMigrateConfig, path: "new", from: "old"},
		{kind: ActionGitInit, path: dir},
	}}
	if !plan.HasPlaceholder() {
		t.Fatal("placeholder was not reported")
	}
	for i, want := range []string{"file ", "migrated from old", "git repository "} {
		if got := plan.Actions[i].Description(); !strings.Contains(got, want) {
			t.Fatalf("description %d = %q, want %q", i, got, want)
		}
	}
	if got := (Unknown{Subject: "runtime", Reason: "not observed"}).Description(); got != "unknown runtime: not observed" {
		t.Fatalf("unknown description = %q", got)
	}
}

func TestNeedsEcosystemResolutionRecognizesExistingConfigs(t *testing.T) {
	dir := t.TempDir()
	if !NeedsEcosystemResolution(dir) {
		t.Fatal("empty project should require ecosystem resolution")
	}
	legacy := filepath.Join(dir, ".config_image.yaml")
	if err := os.WriteFile(legacy, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if NeedsEcosystemResolution(dir) {
		t.Fatal("legacy configuration should suppress ecosystem detection")
	}
	if err := os.Remove(legacy); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "platform-factory.yaml"), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if NeedsEcosystemResolution(dir) {
		t.Fatal("canonical configuration should suppress ecosystem detection")
	}
}

func TestValidateMigrationSourceDetectsPostPlanChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pf.yaml")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	action := Action{from: path, content: []byte("original")}
	if err := validateMigrationSource(action); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateMigrationSource(action); err == nil || !strings.Contains(err.Error(), "changed after planning") {
		t.Fatalf("expected changed-source failure, got %v", err)
	}
}

func TestObservationAndEncodingHelpers(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 30, 0, 0, time.FixedZone("test", 2*60*60))
	observed := Observe(t.TempDir(), now)
	if !observed.GeneratedAt.Equal(now.UTC()) || observed.GitCommit != "" {
		t.Fatalf("observations = %+v", observed)
	}
	if got := yamlQuote("a\\b\"c"); got != `"a\\b\"c"` {
		t.Fatalf("yamlQuote = %q", got)
	}
	if (Plan{Actions: []Action{{kind: ActionMkdir}}}).HasPlaceholder() {
		t.Fatal("ordinary action reported as placeholder")
	}
	if got := (Action{kind: ActionKind(99), path: "opaque"}).Description(); got != "opaque" {
		t.Fatalf("unknown action description = %q", got)
	}
}

func TestValidateMigrationSourceRejectsMissingAndNonRegular(t *testing.T) {
	dir := t.TempDir()
	missing := Action{from: filepath.Join(dir, "missing"), content: []byte("x")}
	if err := validateMigrationSource(missing); err == nil || !strings.Contains(err.Error(), "inspect migrated config") {
		t.Fatalf("expected missing source failure, got %v", err)
	}
	directory := filepath.Join(dir, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateMigrationSource(Action{from: directory}); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("expected non-regular source failure, got %v", err)
	}
}

func TestBuildPlanCoversExistingAndLegacyInputs(t *testing.T) {
	t.Run("missing observation", func(t *testing.T) {
		if _, err := BuildPlan(t.TempDir(), Ecosystem{}, nil, Observations{}); err == nil {
			t.Fatal("expected required observation failure")
		}
	})
	t.Run("already initialized", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "platform-factory.yml"), []byte("version: 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := BuildPlan(dir, Ecosystem{}, nil, testObservations()); err == nil || !strings.Contains(err.Error(), "already initialized") {
			t.Fatalf("expected initialized failure, got %v", err)
		}
	})
	t.Run("legacy config and existing scaffold", func(t *testing.T) {
		dir := t.TempDir()
		legacy := filepath.Join(dir, ".config_image.yaml")
		if err := os.WriteFile(legacy, []byte("version: 1\nlanguage: go\nartifact: app\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{".pf", "policies", "deploy", "dist", "reports", ".git"} {
			if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("existing\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		plan, err := BuildPlan(dir, Ecosystem{Confident: true}, nil, testObservations())
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Actions) != 2 || plan.Actions[0].kind != ActionMigrateConfig {
			t.Fatalf("legacy plan actions = %+v", plan.Actions)
		}
	})
}

func TestRenderConfigIncludesLegacyDisks(t *testing.T) {
	rendered := string(renderConfig(Ecosystem{}, false, &project.LegacyDiskConfig{
		Boot: `disk"boot.qcow2`, Data: []string{"data-a.raw", "data-b.raw"},
	}, testObservations().GeneratedAt))
	for _, want := range []string{"legacy_disks:", "boot:", "data-a.raw", "data-b.raw"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered config missing %q: %s", want, rendered)
		}
	}
}

func TestRollbackRestoresMigratedConfigurationAfterLaterFailure(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "pf.yaml")
	content := []byte("version: 1\nlanguage: go\nartifact: app\n")
	if err := os.WriteFile(legacy, content, 0o640); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(dir, Ecosystem{}, nil, testObservations())
	if err != nil {
		t.Fatal(err)
	}
	// A deterministic failing action after migration exercises the partial
	// application receipt without relying on the host's git installation.
	plan.Actions = append(plan.Actions, Action{kind: ActionMkdir, path: filepath.Join(dir, "missing", "child")})
	receipt, err := Execute(plan)
	if err == nil {
		t.Fatal("expected injected late failure")
	}
	if err := Rollback(receipt); err != nil {
		t.Fatal(err)
	}
	restored, readErr := os.ReadFile(legacy)
	if readErr != nil || string(restored) != string(content) {
		t.Fatalf("legacy config not restored: content=%q err=%v", restored, readErr)
	}
	if _, err := os.Stat(filepath.Join(dir, "platform-factory.yaml")); !os.IsNotExist(err) {
		t.Fatalf("canonical config survived rollback: %v", err)
	}
}

func testObservations() Observations {
	return Observations{GeneratedAt: time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC), GitCommit: "abc123"}
}

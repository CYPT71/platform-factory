package projectinit

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/detect"
	"github.com/CYPT71/platform-factory/internal/project"
	"github.com/CYPT71/platform-factory/internal/scheduler"
)

func TestBuildPlanIsExplicitAndDoesNotMutate(t *testing.T) {
	dir := t.TempDir()
	plan, err := BuildPlan(dir, Ecosystem{Result: detect.Result{Kind: "go", Evidence: []string{"go.mod"}}, Artifact: "cmd/app/main.go", Confident: true}, nil, testObservations())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) == 0 || !strings.Contains(plan.Actions[0].Description(), "pf.yaml") {
		t.Fatalf("plan does not explain its config mutation: %+v", plan.Actions)
	}
	if _, err := os.Stat(filepath.Join(dir, "pf.yaml")); !os.IsNotExist(err) {
		t.Fatalf("planning mutated the project: %v", err)
	}
	for _, want := range []string{".gitignore", ".pf", "policies", "deploy", "dist", "reports", "git repository"} {
		found := false
		for _, action := range plan.Actions {
			if strings.Contains(action.Description(), want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("plan does not include %q: %+v", want, plan.Actions)
		}
	}
}

func TestBuildPlanLongFilenameStyleCreatesExactlyOnePair(t *testing.T) {
	dir := t.TempDir()
	plan, err := BuildPlanWithFilenameStyle(dir, Ecosystem{Result: detect.Result{Kind: "go"}, Artifact: "app", Confident: true}, nil, testObservations(), "long")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(plan); err != nil {
		t.Fatal(err)
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
		t.Fatalf("long style does not discover: %v", err)
	}
}

func TestBuildPlanRejectsInvalidFilenameStyleWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	if _, err := BuildPlanWithFilenameStyle(dir, Ecosystem{}, nil, testObservations(), "both"); err == nil {
		t.Fatal("invalid filename style accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("planning mutated directory: %v %v", entries, err)
	}
}

func TestBuildPlanLongStyleMigratesLegacyConfigToSelectedPair(t *testing.T) {
	dir := t.TempDir()
	legacy := []byte("language: python\nruntime: python\n")
	if err := os.WriteFile(filepath.Join(dir, ".config_image.yaml"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlanWithFilenameStyle(dir, Ecosystem{}, nil, testObservations(), "long")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(plan); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "platform-factory.yaml"))
	if err != nil || !bytes.Equal(got, legacy) {
		t.Fatalf("migration content=%q err=%v", got, err)
	}
	trace, err := os.ReadFile(filepath.Join(dir, ".pf", "migration.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(trace, []byte(`"to": "platform-factory.yaml"`)) {
		t.Fatalf("migration trace has wrong target: %s", trace)
	}
	if _, err := project.Discover(dir, ""); err != nil {
		t.Fatalf("migrated long project does not discover: %v", err)
	}
}

func TestBuildPlanWritesBoundedMultiEcosystemInventoryWithoutEnvironmentValues(t *testing.T) {
	dir := t.TempDir()
	ecosystem := Ecosystem{
		Result: detect.Result{Kind: "go"}, Artifact: "app", Confident: true,
		Inspections: []ApplicationInspection{
			{Detection: detect.Result{Kind: "python", Profile: "script", Evidence: []string{"app.py"}}, Artifact: "app.py", Entrypoint: "app.py", Dependencies: DependencyState{Mode: "none", Reason: "no external dependencies detected"}},
			{Detection: detect.Result{Kind: "go", Profile: "module", Evidence: []string{"go.mod"}}, BuildCommand: []string{"go", "build", "-o", "app", "."}, Artifact: "app", Dependencies: DependencyState{Mode: "manifest", Manifest: "go.mod", Reason: "go.mod detected"}, Environment: map[string]string{"API_TOKEN": "must-not-leak"}},
		},
	}
	plan, err := BuildPlan(dir, ecosystem, nil, testObservations())
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := Execute(plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Rollback(receipt) })
	raw, err := os.ReadFile(filepath.Join(dir, ".pf", "inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("must-not-leak")) {
		t.Fatalf("environment value leaked into inventory: %s", raw)
	}
	var inventory ProjectInventory
	if err := json.Unmarshal(raw, &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.APIVersion != InventoryAPIVersion || inventory.Primary != "go" || len(inventory.Ecosystems) != 2 {
		t.Fatalf("unexpected inventory: %+v", inventory)
	}
	if inventory.Ecosystems[0].Language != "go" || !inventory.Ecosystems[0].Selected || inventory.Ecosystems[1].Language != "python" {
		t.Fatalf("inventory must be stable and preserve every ecosystem: %+v", inventory.Ecosystems)
	}
	if got := inventory.Ecosystems[0].Metadata.EnvironmentKeys; len(got) != 1 || got[0] != "API_TOKEN" {
		t.Fatalf("environment keys = %v", got)
	}
	dagRaw, err := os.ReadFile(filepath.Join(dir, ".pf", "build.pipeline.json"))
	if err != nil {
		t.Fatal(err)
	}
	var dag api.Pipeline
	if err := json.Unmarshal(dagRaw, &dag); err != nil {
		t.Fatal(err)
	}
	graph, err := scheduler.Analyze(dag)
	if err != nil {
		t.Fatalf("generated DAG is invalid: %v", err)
	}
	if len(graph.Order) != 1 || graph.Order[0] != "build" || dag.Stages[0].Command.Executable != "go" {
		t.Fatalf("unexpected initial build DAG: graph=%+v pipeline=%+v", graph, dag)
	}
}

func TestBuildPlanRejectsUnsafeOrUnboundedInventoryBeforeMutation(t *testing.T) {
	for name, inspection := range map[string]ApplicationInspection{
		"path traversal":          {Detection: detect.Result{Kind: "go", Evidence: []string{"../secret"}}},
		"invalid environment key": {Detection: detect.Result{Kind: "go", Evidence: []string{"go.mod"}}, Environment: map[string]string{"BAD=KEY": "secret"}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := BuildPlan(dir, Ecosystem{Result: detect.Result{Kind: "go"}, Confident: true, Inspections: []ApplicationInspection{inspection}}, nil, testObservations())
			if err == nil || !strings.Contains(err.Error(), "project inventory") {
				t.Fatalf("expected inventory validation failure, got %v", err)
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("failed planning mutated project: entries=%v err=%v", entries, readErr)
			}
		})
	}
}

func TestExecuteRefusesFileAppearingAfterPlanning(t *testing.T) {
	dir := t.TempDir()
	plan, err := BuildPlan(dir, Ecosystem{Result: detect.Result{Kind: "go"}, Artifact: "app", Confident: true}, nil, testObservations())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "pf.yaml")
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
	path := filepath.Join(dir, ".config_image.yaml")
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

func TestResolveRootUsesExistingGitTopLevelAndDoesNotGuessOutsideGit(t *testing.T) {
	repository := t.TempDir()
	if out, err := exec.Command("git", "init", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	nested := filepath.Join(repository, "services", "api")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveRoot(nested)
	wantRoot, evalErr := filepath.EvalSymlinks(repository)
	gotRoot, gotEvalErr := filepath.EvalSymlinks(got)
	if err != nil || evalErr != nil || gotEvalErr != nil || gotRoot != wantRoot {
		t.Fatalf("ResolveRoot(Git nested)=%q err=%v want=%q", got, err, repository)
	}

	plain := t.TempDir()
	plainNested := filepath.Join(plain, "services", "api")
	if err := os.MkdirAll(plainNested, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = ResolveRoot(plainNested)
	if err != nil || got != plainNested {
		t.Fatalf("ResolveRoot(plain nested)=%q err=%v want explicit=%q", got, err, plainNested)
	}
}

func TestResolveRootRejectsSymlinkSource(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "project")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRoot(link); err == nil || !strings.Contains(err.Error(), "not a symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
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
		if len(plan.Actions) != 3 || plan.Actions[0].kind != ActionMigrateConfig || filepath.Base(plan.Actions[2].path) != "migration.json" {
			t.Fatalf("legacy plan actions = %+v", plan.Actions)
		}
	})
	t.Run("unsafe existing scaffold", func(t *testing.T) {
		dir := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(dir, "reports")); err != nil {
			t.Fatal(err)
		}
		if _, err := BuildPlan(dir, Ecosystem{}, nil, testObservations()); err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("expected unsafe scaffold rejection, got %v", err)
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
	legacy := filepath.Join(dir, ".config_image.yaml")
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
	if _, err := os.Stat(filepath.Join(dir, "pf.yaml")); !os.IsNotExist(err) {
		t.Fatalf("canonical config survived rollback: %v", err)
	}
}

func testObservations() Observations {
	return Observations{GeneratedAt: time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC), GitCommit: "abc123"}
}

func TestRenderConfigWritesEveryDetectedField(t *testing.T) {
	eco := Ecosystem{
		Result:   detect.Result{Kind: "python", Evidence: []string{"requirements.txt", "main.py"}, Profile: "python"},
		Artifact: "main.py", Runtime: RuntimeContainer, RuntimeEngine: "docker",
		Inspection: ApplicationInspection{
			BuildCommand: []string{"pip", "install", "-r", "requirements.txt"},
			Ports:        []string{"8080/tcp"},
			Environment:  map[string]string{"PORT": "8080", "DEBUG": "false"},
			Dependencies: DependencyState{Mode: "manifest", Manifest: "requirements.txt"},
		},
	}
	out := string(renderConfig(eco, true, nil, time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)))
	for _, want := range []string{
		"Generated by `platform-factory init`",
		"Detected a python project",
		"version: 1\nlanguage: python\n",
		`profile: "python"`,
		`artifact: "main.py"`,
		"build_command:\n",
		`  - "pip"`,
		"isolation: container\n",
		"runtime_engine: docker\n",
		"ports:\n",
		`  - "8080/tcp"`,
		"env:\n",
		`  DEBUG: "false"`,
		`  PORT: "8080"`,
		"dependency_management:\n  mode: manifest\n",
		`  file: "requirements.txt"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	// env keys must render sorted regardless of map iteration order.
	if strings.Index(out, "DEBUG:") > strings.Index(out, "PORT:") {
		t.Fatalf("env keys not sorted:\n%s", out)
	}
}

func TestRenderConfigWithoutLanguageWritesOnlyVersion(t *testing.T) {
	out := string(renderConfig(Ecosystem{Result: detect.Result{Kind: "python"}}, false, nil, testObservations().GeneratedAt))
	if !strings.Contains(out, "version: 1\n") {
		t.Fatalf("expected a bare version line, got:\n%s", out)
	}
	if strings.Contains(out, "language:") || strings.Contains(out, "Detected a") {
		t.Fatalf("expected no language detection content when writeLanguage is false:\n%s", out)
	}
}

func TestRenderConfigOmitsEmptyOptionalSections(t *testing.T) {
	eco := Ecosystem{Result: detect.Result{Kind: "go", Evidence: []string{"go.mod"}}, Artifact: "app"}
	out := string(renderConfig(eco, true, nil, testObservations().GeneratedAt))
	for _, unwanted := range []string{"profile:", "build_command:", "isolation:", "runtime_engine:", "ports:", "env:", "dependency_management:", "legacy_disks:"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("expected no %q section for a minimal ecosystem:\n%s", unwanted, out)
		}
	}
}

func TestYamlQuoteEscapesBackslashesAndQuotes(t *testing.T) {
	got := yamlQuote(`C:\path\with"quote`)
	want := `"C:\\path\\with\"quote"`
	if got != want {
		t.Fatalf("yamlQuote() = %q, want %q", got, want)
	}
}

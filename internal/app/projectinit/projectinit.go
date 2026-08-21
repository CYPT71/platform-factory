// Package projectinit owns the application use-case for scaffolding a local
// Platform Factory project. Command packages adapt flags and interactive UX to
// this package; filesystem planning and mutation live here.
package projectinit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/CYPT71/platform-factory/internal/detect"
	"github.com/CYPT71/platform-factory/internal/policy"
	"github.com/CYPT71/platform-factory/internal/project"
)

type ActionKind int

const (
	ActionMkdir ActionKind = iota
	ActionWriteFile
	ActionMigrateConfig
	ActionGitInit
)

// Ecosystem is the resolved input to a fresh project configuration.
type Ecosystem struct {
	Result        detect.Result
	Artifact      string
	Confident     bool
	Inspection    ApplicationInspection
	Runtime       RuntimeMode
	RuntimeEngine string
	// Inspections preserves every matching plugin observation. Inspection is
	// the operator-selected primary ecosystem used by pf.yaml; the inventory
	// must not discard other ecosystems present in the same repository.
	Inspections []ApplicationInspection
}

// Observations contains nondeterministic facts captured before planning. The
// same observations and project tree produce the same plan bytes.
type Observations struct {
	GeneratedAt time.Time
	GitCommit   string
}

// ResolveRoot returns the repository top-level when source belongs to an
// existing local Git worktree. Outside Git, the explicitly selected directory
// is the root: init never guesses by walking arbitrary parent directories.
func ResolveRoot(source string) (string, error) {
	absolute, err := filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("resolve project source: %w", err)
	}
	if err := validateRoot(absolute); err != nil {
		return "", err
	}
	command := exec.Command("git", "-C", absolute, "rev-parse", "--show-toplevel")
	output, err := command.Output()
	if err != nil {
		return absolute, nil
	}
	root := strings.TrimSpace(string(output))
	if root == "" || strings.ContainsRune(root, 0) {
		return "", errors.New("git returned an invalid empty project root")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve Git project root: %w", err)
	}
	if err := validateRoot(root); err != nil {
		return "", fmt.Errorf("unsafe Git project root: %w", err)
	}
	physicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve physical Git project root: %w", err)
	}
	physicalSource, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve physical project source: %w", err)
	}
	relative, err := filepath.Rel(physicalRoot, physicalSource)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("Git project root %s does not contain source %s", root, absolute)
	}
	return root, nil
}

// Observe captures host facts explicitly so BuildPlan remains deterministic.
func Observe(dir string, now time.Time) Observations {
	observed := Observations{GeneratedAt: now.UTC()}
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output(); err == nil {
		observed.GitCommit = strings.TrimSpace(string(out))
	}
	return observed
}

// Action is one ordered, immutable step of an initialization plan.
type Action struct {
	kind        ActionKind
	path        string
	content     []byte
	from        string
	placeholder bool
}

// Plan is the explicit, reviewable output of inspection. Applying it is a
// separate operation so callers can explain and confirm every mutation first.
// Future component/connection proposals belong beside Actions rather than in
// the CLI; an empty proposal is not evidence that no component exists.
type Plan struct {
	Actions  []Action
	Unknowns []Unknown
	Findings []string
	// System is an optional, canonical proposal produced by inspection. It is
	// deliberately not serialized into the v1 project configuration: accepting
	// a proposal and choosing a v2 on-disk contract are separate decisions.
	System *SystemProposal
}

// Unknown preserves a decision that inspection could not prove. Callers must
// explain it rather than silently turning absence of evidence into a default.
type Unknown struct {
	Subject string
	Reason  string
}

func (u Unknown) Description() string { return fmt.Sprintf("unknown %s: %s", u.Subject, u.Reason) }

// FilterResolvedUnknowns drops the unknowns a caller's own resolution
// already answered - "build.artifact" once artifact is set, and
// "dependencies" once dependencyMode is anything other than "unknown" -
// so a plan never lists an unknown the interactive/flag-driven flow
// already settled.
func FilterResolvedUnknowns(unknowns []Unknown, artifact, dependencyMode string) []Unknown {
	result := unknowns[:0]
	for _, unknown := range unknowns {
		if unknown.Subject == "build.artifact" && artifact != "" {
			continue
		}
		if unknown.Subject == "dependencies" && dependencyMode != "unknown" {
			continue
		}
		result = append(result, unknown)
	}
	return result
}

// Receipt records only state created or removed by one Execute call. Its
// internals stay private so callers cannot forge rollback ownership.
type Receipt struct {
	created    []string
	migrations []migrationBackup
}

type migrationBackup struct {
	path    string
	content []byte
	mode    os.FileMode
}

func (a Action) Description() string {
	switch a.kind {
	case ActionMkdir:
		return "directory " + a.path
	case ActionWriteFile:
		return "file " + a.path
	case ActionMigrateConfig:
		return fmt.Sprintf("file %s (migrated from %s)", a.path, a.from)
	case ActionGitInit:
		return "git repository " + a.path
	default:
		return a.path
	}
}

// HasPlaceholder reports whether the generated configuration still needs an
// artifact path chosen by the user.
func (p Plan) HasPlaceholder() bool {
	for _, action := range p.Actions {
		if action.kind == ActionWriteFile && action.placeholder {
			return true
		}
	}
	return false
}

// NeedsEcosystemResolution avoids detection and prompting when its result
// cannot be used because a configuration already exists or will be migrated.
func NeedsEcosystemResolution(dir string) bool {
	for _, name := range []string{"pf.yaml", "pf.yml", "platform-factory.yaml", "platform-factory.yml"} {
		if exists(filepath.Join(dir, name)) {
			return false
		}
	}
	return findLegacyConfig(dir) == ""
}

// BuildPlan observes dir and returns an ordered plan without mutating it.
func BuildPlan(dir string, ecosystem Ecosystem, legacyDisks *project.LegacyDiskConfig, observed Observations) (Plan, error) {
	return BuildPlanWithFilenameStyle(dir, ecosystem, legacyDisks, observed, "short")
}

// BuildPlanWithFilenameStyle creates exactly one config/lock pair. Keeping the
// choice explicit avoids aliases that can diverge after the first atomic edit.
func BuildPlanWithFilenameStyle(dir string, ecosystem Ecosystem, legacyDisks *project.LegacyDiskConfig, observed Observations, filenameStyle string) (Plan, error) {
	if observed.GeneratedAt.IsZero() {
		return Plan{}, errors.New("project init: generated-at observation is required")
	}
	if err := validateRoot(dir); err != nil {
		return Plan{}, err
	}
	configName, lockName := "pf.yaml", "pf.lock"
	if filenameStyle == "long" {
		configName, lockName = "platform-factory.yaml", "platform-factory.lock"
	} else if filenameStyle != "short" {
		return Plan{}, errors.New("project init: filename style must be short or long")
	}
	canonicalConfig := filepath.Join(dir, configName)
	for _, name := range []string{"pf.yaml", "pf.yml", "platform-factory.yaml", "platform-factory.yml"} {
		if candidate := filepath.Join(dir, name); exists(candidate) {
			return Plan{}, fmt.Errorf("project already initialized: %s exists", candidate)
		}
	}
	for _, name := range []string{"pf.lock", "platform-factory.lock"} {
		if candidate := filepath.Join(dir, name); exists(candidate) {
			return Plan{}, fmt.Errorf("project already initialized: %s exists", candidate)
		}
	}

	var plan []Action
	legacyPath := findLegacyConfig(dir)
	if legacyPath != "" {
		if err := rejectSymlink(legacyPath); err != nil {
			return Plan{}, err
		}
		if _, err := project.Load(legacyPath); err != nil {
			return Plan{}, fmt.Errorf("existing config %s does not load: %w", legacyPath, err)
		}
		raw, err := os.ReadFile(legacyPath)
		if err != nil {
			return Plan{}, fmt.Errorf("read existing config %s: %w", legacyPath, err)
		}
		plan = append(plan, Action{kind: ActionMigrateConfig, path: canonicalConfig, content: raw, from: legacyPath})
	} else {
		plan = append(plan, Action{kind: ActionWriteFile, path: canonicalConfig,
			content:     renderConfig(ecosystem, ecosystem.Confident, legacyDisks, observed.GeneratedAt),
			placeholder: ecosystem.Confident && ecosystem.Artifact == ""})
	}

	planDigest, err := project.CanonicalManifestDigest(plan[0].content)
	if err != nil {
		return Plan{}, err
	}
	plan = append(plan, Action{kind: ActionWriteFile, path: filepath.Join(dir, lockName), content: renderLockFile(observed.GitCommit, planDigest)})
	if !exists(filepath.Join(dir, ".gitignore")) {
		plan = append(plan, Action{kind: ActionWriteFile, path: filepath.Join(dir, ".gitignore"), content: []byte(".pf/\n.platform-factory/\ndist/\nreports/\n")})
	}
	for _, name := range []string{".pf", "policies", "deploy", "dist", "reports"} {
		path := filepath.Join(dir, name)
		if exists(path) {
			if err := requireRealDirectory(path); err != nil {
				return Plan{}, fmt.Errorf("project scaffold %s is unsafe: %w", path, err)
			}
			continue
		}
		plan = append(plan, Action{kind: ActionMkdir, path: path})
		starterName, starterContent := starterFile(name)
		plan = append(plan, Action{kind: ActionWriteFile, path: filepath.Join(path, starterName), content: starterContent})
	}
	if legacyPath == "" && len(ecosystem.Inspections) > 0 {
		inventory, err := renderInventory(ecosystem)
		if err != nil {
			return Plan{}, err
		}
		plan = append(plan, Action{kind: ActionWriteFile, path: filepath.Join(dir, ".pf", "inventory.json"), content: inventory})
		dag, err := renderInitialBuildDAG(ecosystem)
		if err != nil {
			return Plan{}, err
		}
		plan = append(plan, Action{kind: ActionWriteFile, path: filepath.Join(dir, ".pf", "build.pipeline.json"), content: dag})
	}
	if legacyPath != "" {
		trace := struct {
			APIVersion string                    `json:"api_version"`
			MigratedAt string                    `json:"migrated_at"`
			From       string                    `json:"from"`
			To         string                    `json:"to"`
			Normalized bool                      `json:"normalized"`
			Changes    []project.MigrationChange `json:"changes"`
		}{
			APIVersion: "platform-factory.dev/project-migration/v1",
			MigratedAt: observed.GeneratedAt.UTC().Format(time.RFC3339),
			From:       filepath.Base(legacyPath),
			To:         filepath.Base(canonicalConfig),
			Normalized: false,
			Changes: []project.MigrationChange{{
				Field: "filename", From: filepath.Base(legacyPath), To: filepath.Base(canonicalConfig),
				Reason: "use the canonical project configuration filename; document bytes and values are unchanged",
			}},
		}
		encoded, _ := json.MarshalIndent(trace, "", "  ")
		plan = append(plan, Action{kind: ActionWriteFile, path: filepath.Join(dir, ".pf", "migration.json"), content: append(encoded, '\n')})
	}
	gitPath := filepath.Join(dir, ".git")
	if exists(gitPath) {
		info, err := os.Lstat(gitPath)
		if err != nil || (info.Mode()&os.ModeSymlink) != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return Plan{}, fmt.Errorf("existing Git metadata %s is unsafe", gitPath)
		}
	} else {
		plan = append(plan, Action{kind: ActionGitInit, path: dir})
	}
	result := Plan{Actions: plan, Findings: ecosystem.Inspection.Descriptions()}
	if ecosystem.Confident && ecosystem.Artifact == "" && legacyPath == "" {
		result.Unknowns = append(result.Unknowns, Unknown{Subject: "build.artifact", Reason: "not detected or selected"})
	}
	result.Unknowns = append(result.Unknowns, ecosystem.Inspection.Unknowns...)
	proposal, err := InspectSystem(dir)
	if err != nil {
		return Plan{}, err
	}
	return WithSystemProposal(result, proposal)
}

func findLegacyConfig(dir string) string {
	for _, name := range project.ConfigNames {
		if name == "pf.yaml" || name == "pf.yml" || name == "platform-factory.yaml" || name == "platform-factory.yml" {
			continue
		}
		if candidate := filepath.Join(dir, name); exists(candidate) {
			return candidate
		}
	}
	return ""
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

func validateRoot(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect project root %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("project root %s must be a real directory, not a symlink", dir)
	}
	return nil
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlink at %s", path)
	}
	return nil
}

func requireRealDirectory(path string) error {
	if err := rejectSymlink(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("expected directory at %s", path)
	}
	return nil
}

func starterFile(dirName string) (string, []byte) {
	switch dirName {
	case "policies":
		encoded, _ := json.MarshalIndent(policy.Rules{APIVersion: policy.APIVersion, RequirePins: true, RequireHardening: true, RequireSBOM: true}, "", "  ")
		return "default.json", append(encoded, '\n')
	case "deploy":
		return "README.md", []byte("Deployment manifests belong here.\n\nNot yet generated automatically: `platform-factory deploy` builds and applies\na manifest directly rather than reading one from this directory.\n")
	case "reports":
		return "README.md", []byte("Build, freeze and publish reports are written here.\n\n`platform-factory project freeze` currently writes its inventory to\n`.platform-factory/freeze.lock.json` rather than here. `pf build --reports reports`\nwrites `build.json` and, with `--rebuild`, `reproducibility.json`; policy reports\nremain future work.\n")
	case ".pf":
		return "README.md", []byte("Internal Platform Factory state - safe to delete, regenerated by\n`platform-factory`/`pf` commands as needed. Gitignored by the .gitignore this\nproject was initialized with.\n")
	case "dist":
		return "README.md", []byte("Build output lands here (`platform-factory project build`).\nGitignored by the .gitignore this project was initialized with.\n")
	default:
		return "README.md", nil
	}
}

func renderConfig(eco Ecosystem, writeLanguage bool, legacyDisks *project.LegacyDiskConfig, generatedAt time.Time) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by `platform-factory init` (`pf init`) on %s.\n", generatedAt.UTC().Format("2006-01-02"))
	if writeLanguage {
		fmt.Fprintf(&b, "# Detected a %s project (evidence: %s).\n", eco.Result.Kind, strings.Join(eco.Result.Evidence, ", "))
		fmt.Fprintf(&b, "version: 1\nlanguage: %s\n", eco.Result.Kind)
		if profile := strings.TrimSpace(eco.Result.Profile); profile != "" && profile != "unknown" {
			fmt.Fprintf(&b, "profile: %s\n", yamlQuote(profile))
		}
		if eco.Artifact != "" {
			fmt.Fprintf(&b, "artifact: %s\n", yamlQuote(eco.Artifact))
		}
		if len(eco.Inspection.BuildCommand) > 0 {
			b.WriteString("build_command:\n")
			for _, arg := range eco.Inspection.BuildCommand {
				fmt.Fprintf(&b, "  - %s\n", yamlQuote(arg))
			}
		}
		if eco.Runtime != "" {
			fmt.Fprintf(&b, "isolation: %s\n", eco.Runtime)
		}
		if eco.RuntimeEngine != "" {
			fmt.Fprintf(&b, "runtime_engine: %s\n", eco.RuntimeEngine)
		}
		if len(eco.Inspection.Ports) > 0 {
			b.WriteString("ports:\n")
			for _, port := range eco.Inspection.Ports {
				fmt.Fprintf(&b, "  - %s\n", yamlQuote(port))
			}
		}
		if len(eco.Inspection.Environment) > 0 {
			b.WriteString("env:\n")
			keys := make([]string, 0, len(eco.Inspection.Environment))
			for key := range eco.Inspection.Environment {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			for _, key := range keys {
				fmt.Fprintf(&b, "  %s: %s\n", key, yamlQuote(eco.Inspection.Environment[key]))
			}
		}
		if mode := eco.Inspection.Dependencies.Mode; mode != "" {
			fmt.Fprintf(&b, "dependency_management:\n  mode: %s\n", mode)
			if file := eco.Inspection.Dependencies.Manifest; file != "" {
				fmt.Fprintf(&b, "  file: %s\n", yamlQuote(file))
			}
		}
	} else {
		b.WriteString("version: 1\n")
	}
	if legacyDisks != nil {
		b.WriteString("# Legacy VM disk(s) and the fail-closed migration plan retained by `pf init`.\n# Change strategy only after reviewing reports/compatibility.json.\n")
		fmt.Fprintf(&b, "legacy_disks:\n  boot: %s\n  strategy: %s\n", yamlQuote(legacyDisks.Boot), yamlQuote(legacyDisks.Strategy))
		if len(legacyDisks.Data) > 0 {
			b.WriteString("  data:\n")
			for _, path := range legacyDisks.Data {
				fmt.Fprintf(&b, "    - %s\n", yamlQuote(path))
			}
		}
	}
	return []byte(b.String())
}

func yamlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func renderLockFile(gitCommit, planDigest string) []byte {
	lock := project.Lock{Version: project.CurrentLockVersion, GitCommit: gitCommit, PlanDigest: planDigest, Sources: []project.LockedInput{}, Toolchains: []project.LockedInput{}, Bases: []project.LockedInput{}}
	encoded, _ := json.MarshalIndent(lock, "", "  ")
	return append(encoded, '\n')
}

// Execute applies a plan and returns every newly created path in order.
func Execute(plan Plan) (receipt Receipt, err error) {
	for _, action := range plan.Actions {
		switch action.kind {
		case ActionMkdir:
			if err := requireRealDirectory(filepath.Dir(action.path)); err != nil {
				return receipt, fmt.Errorf("unsafe parent for %s: %w", action.path, err)
			}
			if err := os.Mkdir(action.path, 0o755); err != nil {
				return receipt, fmt.Errorf("create %s: %w", action.path, err)
			}
			receipt.created = append(receipt.created, action.path)
		case ActionWriteFile, ActionMigrateConfig:
			if err := requireRealDirectory(filepath.Dir(action.path)); err != nil {
				return receipt, fmt.Errorf("unsafe parent for %s: %w", action.path, err)
			}
			if err := ensureAbsent(action.path); err != nil {
				return receipt, err
			}
			if action.kind == ActionMigrateConfig {
				if err := validateMigrationSource(action); err != nil {
					return receipt, err
				}
			}
			if err := writeValidatedConfig(action.path, action.content); err != nil {
				return receipt, err
			}
			receipt.created = append(receipt.created, action.path)
			if action.kind == ActionMigrateConfig {
				info, statErr := os.Stat(action.from)
				if statErr != nil {
					return receipt, fmt.Errorf("stat migrated config %s: %w", action.from, statErr)
				}
				if err := os.Remove(action.from); err != nil {
					return receipt, fmt.Errorf("remove migrated config %s: %w", action.from, err)
				}
				receipt.migrations = append(receipt.migrations, migrationBackup{path: action.from, content: append([]byte(nil), action.content...), mode: info.Mode().Perm()})
			}
		case ActionGitInit:
			if err := requireRealDirectory(action.path); err != nil {
				return receipt, fmt.Errorf("unsafe git project root: %w", err)
			}
			gitDir := filepath.Join(action.path, ".git")
			if err := os.Mkdir(gitDir, 0o755); err != nil {
				return receipt, fmt.Errorf("reserve git repository %s: %w", gitDir, err)
			}
			receipt.created = append(receipt.created, gitDir)
			if out, err := exec.Command("git", "init", action.path).CombinedOutput(); err != nil {
				return receipt, fmt.Errorf("git init %s: %w: %s", action.path, err, strings.TrimSpace(string(out)))
			}
		}
	}
	return receipt, nil
}

func ensureAbsent(path string) error {
	_, err := os.Lstat(path)
	if err == nil {
		return fmt.Errorf("refusing to overwrite path created after planning: %s", path)
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("inspect target %s: %w", path, err)
	}
	return nil
}

func validateMigrationSource(action Action) error {
	info, err := os.Lstat(action.from)
	if err != nil {
		return fmt.Errorf("inspect migrated config %s: %w", action.from, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("migrated config %s must remain a regular file", action.from)
	}
	current, err := os.ReadFile(action.from)
	if err != nil {
		return fmt.Errorf("read migrated config %s: %w", action.from, err)
	}
	if string(current) != string(action.content) {
		return fmt.Errorf("migrated config %s changed after planning", action.from)
	}
	return nil
}

func writeValidatedConfig(path string, content []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".init-*"+filepath.Ext(path))
	if err != nil {
		return fmt.Errorf("stage %s: %w", path, err)
	}
	name := temporary.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(name)
		}
	}()
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("stage %s: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("stage %s: %w", path, err)
	}
	base := filepath.Base(path)
	if base == "pf.yaml" || base == "pf.yml" || base == "platform-factory.yaml" || base == "platform-factory.yml" {
		if _, err := project.Load(name); err != nil {
			return fmt.Errorf("generated %s does not validate: %w", path, err)
		}
	}
	if err := os.Link(name, path); err != nil {
		return fmt.Errorf("install %s: %w", path, err)
	}
	if err := os.Remove(name); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("remove staged file %s: %w", name, err)
	}
	keep = true
	return nil
}

// Rollback removes created paths in reverse order. It never removes paths that
// were present before Execute.
func Rollback(receipt Receipt) error {
	var failures []error
	for i := len(receipt.created) - 1; i >= 0; i-- {
		if err := os.RemoveAll(receipt.created[i]); err != nil {
			failures = append(failures, fmt.Errorf("remove %s: %w", receipt.created[i], err))
		}
	}
	for i := len(receipt.migrations) - 1; i >= 0; i-- {
		backup := receipt.migrations[i]
		file, err := os.OpenFile(backup.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, backup.mode)
		if err != nil {
			failures = append(failures, fmt.Errorf("restore %s: %w", backup.path, err))
			continue
		}
		if _, err := file.Write(backup.content); err != nil {
			failures = append(failures, fmt.Errorf("restore %s: %w", backup.path, err))
		}
		if err := file.Close(); err != nil {
			failures = append(failures, fmt.Errorf("close restored %s: %w", backup.path, err))
		}
	}
	return errors.Join(failures...)
}

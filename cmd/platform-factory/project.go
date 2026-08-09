package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/CYPT71/secure-oci-base/internal/detect"
	"github.com/CYPT71/secure-oci-base/internal/oci"
	"github.com/CYPT71/secure-oci-base/internal/project"
)

type projectExecutor func(name string, args []string, directory string, stdout, stderr io.Writer) error

func runProject(args []string, stdout, stderr io.Writer, execute projectExecutor, containerExecute containerExecutor, microVMExecute microVMExecutor) int {
	if len(args) == 0 {
		printProjectUsage(stderr)
		return 2
	}
	action := args[0]
	if action == "help" || action == "-h" || action == "--help" {
		printProjectUsage(stdout)
		return 0
	}
	switch action {
	case "show", "plan", "freeze", "build", "run", "launch", "migrate":
	default:
		fmt.Fprintf(stderr, "platform-factory project: unsupported action %q\n", action)
		printProjectUsage(stderr)
		return 2
	}
	flags := flag.NewFlagSet("project "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	configName := flags.String("config", "", "project image YAML/JSON config; otherwise auto-discovered")
	var write *bool
	if action == "migrate" {
		write = flags.Bool("write", false, "rewrite the config file in place instead of printing the migration")
	}
	dryRun := new(bool)
	if action == "freeze" || action == "build" {
		dryRun = flags.Bool("dry-run", false, "explain the action without executing commands or writing files")
	}
	pluginFlags := registerPluginFlags(flags)
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 1 {
		flags.Usage()
		return 2
	}
	start := "."
	if flags.NArg() == 1 {
		start = flags.Arg(0)
	}
	if action == "migrate" {
		return migrateProject(start, *configName, *write, stdout, stderr)
	}
	loaded, err := project.Discover(start, *configName)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project %s: %v\n", action, err)
		if *configName == "" && strings.Contains(err.Error(), "no project image config") {
			return suggestProjectConfig(action, start, stderr)
		}
		return 1
	}
	plugins, pluginErr := pluginFlags.start(context.Background())
	if pluginErr != nil {
		fmt.Fprintf(stderr, "platform-factory project %s: %v\n", action, pluginErr)
		return 1
	}
	defer plugins.Close()
	switch action {
	case "show":
		data, _ := json.MarshalIndent(loaded, "", "  ")
		fmt.Fprintln(stdout, string(data))
		return 0
	case "plan":
		return planProject(loaded, plugins, stdout, stderr)
	case "freeze":
		if *dryRun {
			return explainProjectAction(loaded, plugins, "freeze", stdout, stderr)
		}
		return freezeProject(loaded, plugins, stdout, stderr, execute)
	case "build":
		if *dryRun {
			return explainProjectAction(loaded, plugins, "build", stdout, stderr)
		}
		_, code := buildProject(loaded, stdout, stderr, execute)
		return code
	case "run":
		return runConfiguredProject(loaded, stdout, stderr, execute, containerExecute, microVMExecute)
	case "launch":
		// launch unifies the project lifecycle: freeze when no inventory
		// exists yet, build when the layout is missing, then run.
		if _, statErr := os.Stat(filepath.Join(loaded.Root, ".platform-factory", "freeze.lock.json")); errors.Is(statErr, os.ErrNotExist) {
			if code := freezeProject(loaded, plugins, stdout, stderr, execute); code != 0 {
				return code
			}
		}
		return runConfiguredProject(loaded, stdout, stderr, execute, containerExecute, microVMExecute)
	}
	return 2
}

// resolveFreezeSteps prefers the built-in freeze adapters and falls
// back to a plugin with the freeze capability when the language has no
// built-in adapter, keeping the built-in error when neither applies.
func resolveFreezeSteps(loaded project.Loaded, plugins *pluginHost) ([]freezeStep, error) {
	steps, err := freezeSteps(loaded)
	if err == nil || plugins == nil || !errors.Is(err, errNoBuiltinFreezeAdapter) {
		return steps, err
	}
	pluginSteps, _, pluginErr := plugins.freeze(context.Background(), loaded.Config.Language, loaded.Root)
	if pluginErr != nil {
		if errors.Is(pluginErr, errNoPluginFreeze) {
			return nil, err
		}
		return nil, pluginErr
	}
	return pluginSteps, nil
}

// explainProjectAction is the --dry-run backend for the mutating project
// actions: it prints the exact commands and files the action would touch
// and performs no mutation.
func explainProjectAction(loaded project.Loaded, plugins *pluginHost, action string, stdout, stderr io.Writer) int {
	result := map[string]any{
		"action": action, "config": loaded.File, "dry_run": true, "valid": true,
	}
	switch action {
	case "freeze":
		steps, err := resolveFreezeSteps(loaded, plugins)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory project freeze: %v\n", err)
			return 2
		}
		commands := make([][]string, 0, len(steps))
		for _, step := range steps {
			commands = append(commands, append([]string(nil), step.args...))
		}
		result["commands"] = commands
		result["writes"] = []string{filepath.Join(".platform-factory", "freeze.lock.json")}
	case "build":
		result["command"] = append([]string(nil), loaded.Config.BuildCommand...)
		result["output"] = loaded.Output()
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	fmt.Fprintln(stdout, string(data))
	return 0
}

// runConfiguredProject builds a missing layout, then runs it with the
// configured isolation and runtime.
func runConfiguredProject(loaded project.Loaded, stdout, stderr io.Writer, execute projectExecutor, containerExecute containerExecutor, microVMExecute microVMExecutor) int {
	if _, err := os.Stat(loaded.Output()); errors.Is(err, os.ErrNotExist) {
		if _, code := buildProject(loaded, stdout, stderr, execute); code != 0 {
			return code
		}
	} else if err != nil {
		fmt.Fprintf(stderr, "platform-factory project run: stat image: %v\n", err)
		return 1
	}
	if loaded.Config.Isolation == "microvm" {
		runtimeArgs := []string{"run", "--layout", loaded.Output()}
		for _, port := range loaded.Config.Ports {
			runtimeArgs = append(runtimeArgs, "-p", port)
		}
		return runMicroVM(runtimeArgs, stdout, stderr, microVMExecute)
	}
	network := loaded.Config.Network
	if len(loaded.Config.Ports) > 0 && network == "none" {
		network = "bridge"
	}
	runtimeArgs := []string{"--runtime", loaded.Config.RuntimeEngine, "--network", network}
	for _, port := range loaded.Config.Ports {
		runtimeArgs = append(runtimeArgs, "-p", port)
	}
	runtimeArgs = append(runtimeArgs, loaded.Output())
	return runContainer(runtimeArgs, stdout, stderr, containerExecute)
}

func printProjectUsage(output io.Writer) {
	fmt.Fprintln(output, `usage: platform-factory project <show|plan|freeze|build|run|migrate> [--config FILE] [DIRECTORY]

Actions:
  show     print the resolved project configuration
  plan     explain freeze, build and runtime actions without changing files
  freeze   lock dependencies and write .platform-factory/freeze.lock.json
  build    build the configured OCI layout
  run      build a missing layout, then run it as configured
  migrate  rewrite the config to the current schema version (--write applies)

freeze and build accept --dry-run to explain the action without mutation.

Friendly commands:
  platform-factory plan [--config FILE] [DIRECTORY]
  platform-factory launch --dry-run [--config FILE] [DIRECTORY]
  platform-factory launch [--config FILE] [DIRECTORY]   freeze + build + run as needed`)
}

// migrateProject rewrites a project config to the current schema
// version. Without --write it is read-only: it prints the migrated
// document and the recorded changes. With --write it validates the
// migrated document by loading it, then installs it atomically.
func migrateProject(start, configName string, write bool, stdout, stderr io.Writer) int {
	filename := configName
	if filename == "" {
		found, err := project.FindConfigFile(start)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory project migrate: %v\n", err)
			return 1
		}
		filename = found
	}
	raw, err := os.ReadFile(filename)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project migrate: %v\n", err)
		return 1
	}
	migrated, changes, err := project.Migrate(raw)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project migrate: %v\n", err)
		return 1
	}
	temporary, err := os.CreateTemp(filepath.Dir(filename), ".migrate-*"+filepath.Ext(filename))
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project migrate: %v\n", err)
		return 1
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if _, err := temporary.Write(migrated); err != nil {
		_ = temporary.Close()
		fmt.Fprintf(stderr, "platform-factory project migrate: %v\n", err)
		return 1
	}
	if err := temporary.Close(); err != nil {
		fmt.Fprintf(stderr, "platform-factory project migrate: %v\n", err)
		return 1
	}
	if _, err := project.Load(temporaryName); err != nil {
		fmt.Fprintf(stderr, "platform-factory project migrate: migrated config does not validate: %v\n", err)
		return 1
	}
	applied := false
	if write && len(changes) > 0 {
		if err := os.Rename(temporaryName, filename); err != nil {
			fmt.Fprintf(stderr, "platform-factory project migrate: %v\n", err)
			return 1
		}
		removeTemporary = false
		applied = true
	}
	result, _ := json.MarshalIndent(map[string]any{
		"config": filename, "version": project.CurrentConfigVersion,
		"changes": changes, "applied": applied,
		"document": string(migrated), "valid": true,
	}, "", "  ")
	fmt.Fprintln(stdout, string(result))
	return 0
}

// suggestProjectConfig turns a missing-config failure into an actionable
// next step by classifying the project directory. Exit code 2 marks an
// ambiguous directory that needs an explicit language decision; 1 keeps
// the original missing-config failure.
func suggestProjectConfig(action string, start string, stderr io.Writer) int {
	result, err := detect.Path(start)
	if err != nil || (result.Kind == "unknown" && !result.Ambiguous) {
		return 1
	}
	if result.Ambiguous {
		fmt.Fprintf(stderr,
			"detected multiple ecosystems (%s); write a %s with an explicit language to resolve the ambiguity\n",
			strings.Join(result.Candidates, ", "), project.ConfigNames[0])
		return 2
	}
	fmt.Fprintf(stderr, `detected a %s project (evidence: %s); a minimal %s looks like:

version: 1
language: %s
artifact: RELATIVE/PATH/TO/EXECUTABLE-OR-ENTRY
`, result.Kind, strings.Join(result.Evidence, ", "), project.ConfigNames[0], result.Kind)
	return 1
}

func planProject(loaded project.Loaded, plugins *pluginHost, stdout, stderr io.Writer) int {
	steps, err := resolveFreezeSteps(loaded, plugins)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project plan: %v\n", err)
		return 2
	}
	freezeCommands := make([][]string, 0, len(steps))
	for _, step := range steps {
		freezeCommands = append(freezeCommands, append([]string(nil), step.args...))
	}
	phases := []map[string]any{
		{"name": "freeze", "commands": freezeCommands, "mutates_project": len(steps) > 0},
		{"name": "build", "command": append([]string(nil), loaded.Config.BuildCommand...), "output": loaded.Output()},
		{"name": "run", "isolation": loaded.Config.Isolation, "runtime": loaded.Config.RuntimeEngine},
	}
	result := map[string]any{
		"api_version": "platform-factory.dev/project-plan/v1",
		"config":      loaded.File,
		"image":       loaded.Config.Image + ":" + loaded.Config.Tag,
		"language":    loaded.Config.Language,
		"network":     loaded.Config.Network,
		"phases":      phases,
		"platform":    loaded.Config.Platform,
		"ports":       loaded.Config.Ports,
		"valid":       true,
	}
	if detected, detectErr := detect.Path(loaded.Root); detectErr == nil && detected.Kind != "unknown" {
		result["detected"] = map[string]any{
			"kind": detected.Kind, "ambiguous": detected.Ambiguous,
			"candidates": detected.Candidates, "evidence": detected.Evidence,
			"matches_language": !detected.Ambiguous && strings.EqualFold(detected.Kind, loaded.Config.Language),
		}
	}
	if plugins != nil {
		if notes := plugins.planNotes(context.Background(), loaded.Config.Language, loaded.Root); len(notes) > 0 {
			result["plugin_notes"] = notes
		}
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project plan: encode result: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, string(data))
	return 0
}

func projectLaunchAction(args []string) (string, []string, error) {
	action := "launch"
	remaining := make([]string, 0, len(args))
	for _, argument := range args {
		switch argument {
		case "--dry-run", "--plan":
			if action == "plan" {
				return "", nil, errors.New("--dry-run/--plan may be specified only once")
			}
			action = "plan"
		default:
			remaining = append(remaining, argument)
		}
	}
	return action, remaining, nil
}

type freezeStep struct {
	args   []string
	output string
}

func freezeProject(loaded project.Loaded, plugins *pluginHost, stdout, stderr io.Writer, execute projectExecutor) int {
	steps, err := resolveFreezeSteps(loaded, plugins)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project freeze: %v\n", err)
		return 2
	}
	for _, step := range steps {
		output := stderr
		var outputFile *os.File
		if step.output != "" {
			filename := loaded.Resolve(step.output)
			if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
				fmt.Fprintf(stderr, "platform-factory project freeze: %v\n", err)
				return 1
			}
			outputFile, err = os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				fmt.Fprintf(stderr, "platform-factory project freeze: %v\n", err)
				return 1
			}
			output = outputFile
		}
		fmt.Fprintf(stderr, "platform-factory: freeze: %s\n", formatCommand(step.args[0], step.args[1:]))
		commandErr := execute(step.args[0], step.args[1:], loaded.Root, output, stderr)
		if outputFile != nil {
			if closeErr := outputFile.Close(); commandErr == nil {
				commandErr = closeErr
			}
		}
		if commandErr != nil {
			fmt.Fprintf(stderr, "platform-factory project freeze: %s failed: %v\n", step.args[0], commandErr)
			return 1
		}
	}
	inventory, err := loaded.WriteFreezeInventory()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project freeze: inventory: %v\n", err)
		return 1
	}
	result, _ := json.MarshalIndent(map[string]any{
		"config": loaded.File, "freeze_lock": inventory,
		"language": loaded.Config.Language, "valid": true,
	}, "", "  ")
	fmt.Fprintln(stdout, string(result))
	return 0
}

// errNoBuiltinFreezeAdapter marks freezeSteps' default case so
// resolveFreezeSteps can detect it with errors.Is rather than matching
// on the error's user-facing text - that text is free to be rewritten
// for clarity without silently breaking the plugin-freeze fallback.
var errNoBuiltinFreezeAdapter = errors.New("no built-in freeze adapter")

func freezeSteps(loaded project.Loaded) ([]freezeStep, error) {
	if len(loaded.Config.FreezeCommand) > 0 {
		return []freezeStep{{args: loaded.Config.FreezeCommand}}, nil
	}
	exists := func(name string) bool {
		info, err := os.Stat(filepath.Join(loaded.Root, name))
		return err == nil && info.Mode().IsRegular()
	}
	switch strings.ToLower(loaded.Config.Language) {
	case "go", "golang":
		return []freezeStep{{args: []string{"go", "mod", "tidy"}}, {args: []string{"go", "mod", "vendor"}}}, nil
	case "node", "nodejs", "javascript", "typescript":
		if exists("package-lock.json") || exists("npm-shrinkwrap.json") {
			return []freezeStep{{args: []string{"npm", "ci", "--ignore-scripts"}}}, nil
		}
		return []freezeStep{
			{args: []string{"npm", "install", "--package-lock-only", "--ignore-scripts"}},
			{args: []string{"npm", "ci", "--ignore-scripts"}},
		}, nil
	case "python":
		requirements := "requirements.lock"
		if !exists(requirements) {
			requirements = "requirements.txt"
		}
		if !exists(requirements) {
			return nil, errors.New("no requirements.lock or requirements.txt found here - " +
				"add one of those files, or add a freeze_command to platform-factory.yaml, e.g.:\n" +
				"  freeze_command: [\"pip\", \"freeze\", \">\", \"requirements.txt\"]")
		}
		target := ".platform-factory/deps/python"
		return []freezeStep{
			{args: []string{"python", "-m", "pip", "install", "--requirement", requirements, "--target", target}},
			{args: []string{"python", "-m", "pip", "freeze", "--path", target}, output: "requirements.lock"},
		}, nil
	case "java":
		if exists("mvnw") {
			return []freezeStep{{args: []string{wrapperCommand("./mvnw"), "-B", "dependency:go-offline"}}}, nil
		}
		if exists("gradlew") {
			return []freezeStep{{args: []string{wrapperCommand("./gradlew"), "dependencies", "--write-locks"}}}, nil
		}
		if exists("pom.xml") {
			return []freezeStep{{args: []string{"mvn", "-B", "dependency:go-offline"}}}, nil
		}
		return nil, errors.New("no Maven (pom.xml/mvnw) or Gradle (gradlew) files found here - " +
			"add one of those, or add a freeze_command to platform-factory.yaml")
	case "dotnet", "csharp", "fsharp":
		return []freezeStep{{args: []string{"dotnet", "restore", "--use-lock-file"}}}, nil
	case "rust":
		return []freezeStep{{args: []string{"cargo", "generate-lockfile"}}, {args: []string{"cargo", "fetch", "--locked"}}}, nil
	case "ruby":
		return []freezeStep{{args: []string{"bundle", "lock"}}, {args: []string{"bundle", "cache", "--all"}}}, nil
	case "php":
		return []freezeStep{{args: []string{"composer", "install", "--no-dev", "--prefer-dist", "--no-interaction"}}}, nil
	case "compiled":
		return nil, nil
	case "custom":
		return nil, errors.New(`language: custom means you write the freeze step yourself - add a freeze_command to platform-factory.yaml, e.g.:` +
			"\n  freeze_command: [\"make\", \"deps\"]")
	default:
		return nil, fmt.Errorf("%w: %q isn't a language platform-factory knows how to freeze automatically (try: go, node, python, java, dotnet, rust, ruby, php, or custom) - "+
			"add a freeze_command to platform-factory.yaml instead", errNoBuiltinFreezeAdapter, loaded.Config.Language)
	}
}

func wrapperCommand(value string) string {
	if runtime.GOOS == "windows" {
		return value + ".bat"
	}
	return value
}

func buildProject(loaded project.Loaded, stdout, stderr io.Writer, execute projectExecutor) (string, int) {
	if len(loaded.Config.BuildCommand) > 0 {
		command := loaded.Config.BuildCommand
		fmt.Fprintf(stderr, "platform-factory: build: %s\n", formatCommand(command[0], command[1:]))
		if err := execute(command[0], command[1:], loaded.Root, stderr, stderr); err != nil {
			fmt.Fprintf(stderr, "platform-factory project build: command failed: %v\n", err)
			return "", 1
		}
	}
	field := "artifact"
	binaryName := loaded.Config.Artifact
	if loaded.Config.Runtime != "" {
		field, binaryName = "runtime", loaded.Config.Runtime
	}
	binary := loaded.Resolve(binaryName)
	if info, statErr := os.Stat(binary); statErr != nil || !info.Mode().IsRegular() {
		fmt.Fprintf(stderr, "platform-factory project build: the %q field in %s points to %q, but that file doesn't exist (looked at %s)\n",
			field, filepath.Base(loaded.File), binaryName, binary)
		if len(loaded.Config.BuildCommand) > 0 {
			fmt.Fprintf(stderr, "  the build_command above ran, but didn't produce that file - check where it actually writes its output\n")
		} else {
			fmt.Fprintf(stderr, "  fix: build your app so that file exists, or correct the %q path in %s\n", field, filepath.Base(loaded.File))
		}
		return "", 1
	}
	extraFiles, err := loaded.ImageFiles()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project build: dependencies: %v\n", err)
		return "", 1
	}
	languageLayer, cleanupLanguageLayer, err := languagePluginLayer(loaded, stderr, execute, resolveLoadedPlugin)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project build: %v\n", err)
		return "", 1
	}
	defer cleanupLanguageLayer()
	var extraLayers []string
	if languageLayer != "" {
		extraLayers = []string{languageLayer}
	}
	osName, architecture, err := parsePlatform(loaded.Config.Platform)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project build: %v\n", err)
		return "", 2
	}
	entrypoint := loaded.Config.Entrypoint
	if entrypoint == "" {
		entrypoint = "/app/" + filepath.Base(binary)
		if loaded.Config.Runtime != "" {
			entrypoint = "/runtime/" + filepath.Base(binary)
		}
	}
	profile := loaded.Config.Profile
	if profile == "" {
		profile = projectProfile(loaded.Config.Language)
	}
	digest, err := oci.Build(oci.Options{
		Binary: binary, Output: loaded.Output(), Architecture: architecture, OS: osName,
		Entrypoint: entrypoint, Profile: profile, ImageName: loaded.Config.Image,
		Tag: loaded.Config.Tag, Created: time.Unix(0, 0), Args: loaded.Config.Args,
		Env: loaded.Config.Env, User: loaded.Config.User, ExtraFiles: extraFiles,
		Compression: "fast", TraceID: os.Getenv("PLATFORM_FACTORY_TRACE_ID"),
		SemanticLayers: loaded.Config.SemanticLayers, ExtraLayers: extraLayers,
		Observer: cliObserver(stderr),
	})
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project build: %v\n", err)
		return "", 1
	}
	result, _ := json.MarshalIndent(map[string]any{
		"config": loaded.File, "digest": digest, "isolation": loaded.Config.Isolation,
		"layout": loaded.Output(), "reference": loaded.Config.Image + ":" + loaded.Config.Tag,
		"valid": true,
	}, "", "  ")
	fmt.Fprintln(stdout, string(result))
	return digest, 0
}

// projectProfile maps a project language to a runtime profile. Go, Rust
// and other compiled languages deliberately map to "static": they produce
// ELF executables, and the ELF detection in internal/oci picks
// static/glibc/musl from the actual binary rather than from the language
// name.
func projectProfile(language string) string {
	switch strings.ToLower(language) {
	case "python":
		return "python"
	case "node", "nodejs", "javascript", "typescript":
		return "node"
	case "java":
		return "java"
	case "dotnet", "csharp", "fsharp":
		return "dotnet"
	case "ruby":
		return "ruby"
	case "php":
		return "php"
	default:
		return "static"
	}
}

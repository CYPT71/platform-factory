package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"

	"github.com/CYPT71/platform-factory/cmd/tui/buildtui"
	buildapp "github.com/CYPT71/platform-factory/internal/app/build"
	projectapp "github.com/CYPT71/platform-factory/internal/app/project"
	"github.com/CYPT71/platform-factory/internal/atomicfile"
	"github.com/CYPT71/platform-factory/internal/budget"
	"github.com/CYPT71/platform-factory/internal/detect"
	"github.com/CYPT71/platform-factory/internal/layout"
	"github.com/CYPT71/platform-factory/internal/observability"
	"github.com/CYPT71/platform-factory/internal/project"
	runtimeapp "github.com/CYPT71/platform-factory/internal/runtime"
	"github.com/CYPT71/platform-factory/internal/shellquote"
	"github.com/CYPT71/platform-factory/oci"
)

type projectExecutor func(name string, args []string, directory string, stdout, stderr io.Writer) error

func runProject(args []string, stdout, stderr io.Writer, execute projectExecutor, containerExecute containerExecutor, microVMExecute microVMExecutor) int {
	return runProjectContext(context.Background(), args, stdout, stderr, execute, containerExecute, microVMExecute)
}

func runProjectContext(ctx context.Context, args []string, stdout, stderr io.Writer, execute projectExecutor, containerExecute containerExecutor, microVMExecute microVMExecutor) int {
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
	freezeKernel, freezeInitramfs := new(string), new(string)
	var write *bool
	if action == "migrate" {
		write = flags.Bool("write", false, "rewrite the config file in place instead of printing the migration")
	}
	dryRun := new(bool)
	maxWallClock := new(time.Duration)
	maxCPU := new(time.Duration)
	maxMemory := new(string)
	if action == "freeze" || action == "build" {
		dryRun = flags.Bool("dry-run", false, "explain the action without executing commands or writing files")
	}
	if action == "freeze" {
		freezeKernel = flags.String("kernel", "", "project-relative kernel to pin for microvm isolation")
		freezeInitramfs = flags.String("initramfs", "", "project-relative initramfs to pin for microvm isolation")
	}
	if action == "build" {
		maxWallClock = flags.Duration("max-wall-clock", 0, "maximum build wall-clock duration (0 disables)")
		maxCPU = flags.Duration("max-cpu", 0, "maximum build CPU duration (0 disables)")
		maxMemory = flags.String("max-memory", "0", "maximum process heap, for example 512MiB (0 disables)")
	}
	watch := new(bool)
	runtimeOverride := new(string)
	if action == "run" || action == "launch" {
		watch = flags.Bool("watch", false, "keep running, rebuilding and restarting the container whenever a source file changes")
		runtimeOverride = flags.String("runtime", "", "container runtime: docker or podman (overrides the project config's runtime_engine)")
	}
	yes := new(bool)
	if action == "build" {
		yes = flags.Bool("yes", false, "skip the interactive image reference confirmation")
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
	var resourceBudget budget.Budget
	if action == "build" {
		memory, budgetErr := buildapp.ParseByteLimit(*maxMemory)
		if budgetErr != nil || *maxWallClock < 0 || *maxCPU < 0 {
			if budgetErr == nil {
				budgetErr = errors.New("time budgets cannot be negative")
			}
			fmt.Fprintf(stderr, "platform-factory project build: invalid resource budget: %v\n", budgetErr)
			return 2
		}
		resourceBudget = budget.Budget{WallClock: *maxWallClock, CPU: *maxCPU, Memory: memory}
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
	if action == "build" && loaded.Config.DependencyManagement != nil {
		switch loaded.Config.DependencyManagement.Mode {
		case "unresolved", "unknown":
			fmt.Fprintf(stderr, "platform-factory project build: dependency state is %s; resolve it in %s before building\n", loaded.Config.DependencyManagement.Mode, filepath.Base(loaded.File))
			return 2
		}
	}
	plugins, pluginErr := pluginFlags.start(context.Background())
	if pluginErr != nil {
		fmt.Fprintf(stderr, "platform-factory project %s: %v\n", action, pluginErr)
		return 1
	}
	defer plugins.Close()
	switch action {
	case "show":
		data, _ := json.MarshalIndent(struct {
			APIVersion string `json:"api_version"`
			project.Loaded
		}{APIVersion: cliOutputAPIVersion, Loaded: loaded}, "", "  ")
		fmt.Fprintln(stdout, string(data))
		return 0
	case "plan":
		return planProject(loaded, plugins, stdout, stderr)
	case "freeze":
		if *dryRun {
			return explainProjectAction(loaded, plugins, "freeze", budget.Budget{}, stdout, stderr)
		}
		return freezeProjectWithBootPins(loaded, plugins, *freezeKernel, *freezeInitramfs, stdout, stderr, execute)
	case "build":
		if *dryRun {
			return explainProjectAction(loaded, plugins, "build", resourceBudget, stdout, stderr)
		}
		// This confirmation belongs only to the direct `pf project build`
		// entry point, never inside buildProjectContext/
		// buildProjectContextWithBudget themselves - those are also
		// called by every automatic rebuild-on-run/--watch cycle and the
		// release path's reproducibility double-build, none of which
		// should ever block on interactive input.
		if !*yes && isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd()) {
			confirmed, err := buildtui.Confirm(loaded.Config.Image, loaded.Config.Tag)
			if err != nil {
				fmt.Fprintf(stderr, "platform-factory project build: confirm image reference: %v\n", err)
				return 1
			}
			if !confirmed.Confirmed {
				fmt.Fprintln(stderr, "platform-factory project build: canceled")
				return 1
			}
			loaded.Config.Image, loaded.Config.Tag = confirmed.Image, confirmed.Tag
		}
		_, code := buildProjectContextWithBudget(ctx, loaded, stdout, stderr, execute, resourceBudget)
		return code
	case "run":
		if *runtimeOverride != "" {
			if *runtimeOverride != "docker" && *runtimeOverride != "podman" {
				fmt.Fprintln(stderr, "platform-factory project run: runtime must be docker or podman")
				return 2
			}
			loaded.Config.RuntimeEngine = *runtimeOverride
		}
		if *watch {
			watchCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return runConfiguredProjectWatch(watchCtx, loaded, plugins, stdout, stderr, execute, containerExecute, defaultWatchPollInterval)
		}
		return runConfiguredProject(ctx, loaded, plugins, stdout, stderr, execute, containerExecute, microVMExecute)
	case "launch":
		if *runtimeOverride != "" {
			if *runtimeOverride != "docker" && *runtimeOverride != "podman" {
				fmt.Fprintln(stderr, "platform-factory project launch: runtime must be docker or podman")
				return 2
			}
			loaded.Config.RuntimeEngine = *runtimeOverride
		}
		// launch unifies the project lifecycle: freeze when no inventory
		// exists yet, build when the layout is missing, then run.
		if _, statErr := os.Stat(filepath.Join(loaded.Root, ".platform-factory", "freeze.lock.json")); errors.Is(statErr, os.ErrNotExist) {
			if code := freezeProject(loaded, plugins, stdout, stderr, execute); code != 0 {
				return code
			}
		}
		if *watch {
			watchCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return runConfiguredProjectWatch(watchCtx, loaded, plugins, stdout, stderr, execute, containerExecute, defaultWatchPollInterval)
		}
		return runConfiguredProject(ctx, loaded, plugins, stdout, stderr, execute, containerExecute, microVMExecute)
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
func explainProjectAction(loaded project.Loaded, plugins *pluginHost, action string, resourceBudget budget.Budget, stdout, stderr io.Writer) int {
	result := map[string]any{
		"api_version": cliOutputAPIVersion,
		"action":      action, "config": loaded.File, "dry_run": true, "valid": true,
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
		capabilities, err := projectapp.AssessBuildCapabilities(context.Background(), loaded)
		if err != nil {
			result["valid"] = false
			result["blockers"] = []string{err.Error()}
			result["capabilities"] = capabilities
			data, _ := json.MarshalIndent(result, "", "  ")
			fmt.Fprintln(stdout, string(data))
			return 2
		}
		result["capabilities"] = capabilities
		result["command"] = append([]string(nil), loaded.Config.BuildCommand...)
		result["output"] = loaded.Output()
		result["resource_budget"] = buildapp.ResourceBudgetPlan(resourceBudget)
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	fmt.Fprintln(stdout, string(data))
	return 0
}

// rebuildProjectLayout removes any existing layout at loaded.Output()
// before building - oci.Build refuses to write into a
// directory that already exists (a deliberate anti-clobber safety for
// pf project build's own direct use), so a caller that has already
// decided a rebuild is warranted (loaded.Output() is missing or, per
// projectapp.NeedsRebuild, stale) must clear the old one first. Removing a
// path that doesn't exist is a harmless no-op.
//
// It also re-freezes automatically when the frozen inventory has gone
// stale - unlike `pf project build`'s own direct use (which still fails
// loudly and asks the caller to run `pf freeze` themselves, since that
// command is often run deliberately, e.g. in CI, where silently
// re-pinning dependencies without asking would hide real drift), pf
// run/launch/--watch's whole point is a developer editing a real source
// file and having it just work - the same file that made the freeze
// stale in the first place is legitimate, not something to fail on.
func rebuildProjectLayout(ctx context.Context, loaded project.Loaded, plugins *pluginHost, stdout, stderr io.Writer, execute projectExecutor) int {
	if projectapp.RequiresFrozenInputs(loaded) {
		if err := loaded.VerifyFreezeInventory(); err != nil {
			if code := freezeProject(loaded, plugins, stdout, stderr, execute); code != 0 {
				return code
			}
		}
	}
	if err := os.RemoveAll(loaded.Output()); err != nil {
		fmt.Fprintf(stderr, "platform-factory project run: remove stale output: %v\n", err)
		return 1
	}
	_, code := buildProjectContext(ctx, loaded, stdout, stderr, execute)
	return code
}

// runConfiguredProject rebuilds loaded's layout when it is missing or
// stale (see projectapp.NeedsRebuild), then runs it with the configured
// isolation and runtime.
func runConfiguredProject(ctx context.Context, loaded project.Loaded, plugins *pluginHost, stdout, stderr io.Writer, execute projectExecutor, containerExecute containerExecutor, microVMExecute microVMExecutor) int {
	rebuild, err := projectapp.NeedsRebuild(loaded)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project run: stat image: %v\n", err)
		return 1
	}
	if rebuild {
		if code := rebuildProjectLayout(ctx, loaded, plugins, stdout, stderr, execute); code != 0 {
			return code
		}
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
	return runContainer(ctx, runtimeArgs, stdout, stderr, containerExecute)
}

// defaultWatchPollInterval is how often runConfiguredProjectWatch
// re-checks projectapp.NeedsRebuild while a watched container is running. A
// plain stat-based poll (not an OS file-notification API) matches this
// repository's preference for minimal external dependencies - no new
// fsnotify-family dependency, no per-OS watcher backend - and is more
// than fast enough for a development rebuild loop.
const defaultWatchPollInterval = time.Second

// runConfiguredProjectWatch is runConfiguredProject's --watch mode: it
// keeps running loaded's container, rebuilding and restarting it
// whenever projectapp.NeedsRebuild reports a source file changed, until ctx
// is canceled (SIGINT/SIGTERM - see the signal.NotifyContext this is
// called with) or the container exits on its own. It does not support
// microvm isolation yet - that backend's start/stop lifecycle isn't
// wired into this loop. pollInterval is a parameter (tests pass a short
// one) rather than a shared package var, so nothing here needs to
// coordinate with a background poller goroutine that may still be
// unwinding after this function returns.
func runConfiguredProjectWatch(ctx context.Context, loaded project.Loaded, plugins *pluginHost, stdout, stderr io.Writer, execute projectExecutor, containerExecute containerExecutor, pollInterval time.Duration) int {
	if loaded.Config.Isolation == "microvm" {
		fmt.Fprintln(stderr, "platform-factory project run: --watch does not support microvm isolation yet")
		return 2
	}
	containerName := projectapp.WatchContainerName(loaded)
	defer stopWatchedContainer(loaded.Config.RuntimeEngine, containerName, containerExecute)

	for {
		if rebuild, err := projectapp.NeedsRebuild(loaded); err != nil {
			fmt.Fprintf(stderr, "platform-factory project run: stat image: %v\n", err)
			return 1
		} else if rebuild {
			if code := rebuildProjectLayout(ctx, loaded, plugins, stdout, stderr, execute); code != 0 {
				return code
			}
		}
		if ctx.Err() != nil {
			return 0
		}

		network := loaded.Config.Network
		if len(loaded.Config.Ports) > 0 && network == "none" {
			network = "bridge"
		}
		runtimeArgs := []string{"--runtime", loaded.Config.RuntimeEngine, "--network", network, "--name", containerName}
		for _, port := range loaded.Config.Ports {
			runtimeArgs = append(runtimeArgs, "-p", port)
		}
		runtimeArgs = append(runtimeArgs, loaded.Output())

		exited := make(chan int, 1)
		go func() { exited <- runContainer(ctx, runtimeArgs, stdout, stderr, containerExecute) }()

		select {
		case code := <-exited:
			// The container ended on its own (it crashed, or ran to
			// completion) rather than being stopped for a rebuild -
			// nothing left to watch for.
			return code
		case <-ctx.Done():
			stopWatchedContainer(loaded.Config.RuntimeEngine, containerName, containerExecute)
			<-exited
			return 0
		case <-watchForChange(ctx, loaded, pollInterval):
			fmt.Fprintln(stderr, "platform-factory project run: source changed, rebuilding")
			stopWatchedContainer(loaded.Config.RuntimeEngine, containerName, containerExecute)
			<-exited
		}
	}
}

// watchForChange polls projectapp.NeedsRebuild every pollInterval and sends
// once when it first reports true, or closes without sending if ctx is
// canceled first.
func watchForChange(ctx context.Context, loaded project.Loaded, pollInterval time.Duration) <-chan struct{} {
	changed := make(chan struct{})
	go func() {
		defer close(changed)
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if rebuild, err := projectapp.NeedsRebuild(loaded); err == nil && rebuild {
					changed <- struct{}{}
					return
				}
			}
		}
	}()
	return changed
}

// stopWatchedContainer stops the named container; --rm (always present
// in runContainer's own hardcoded runtimeArgs) makes the runtime remove
// it once stopped, so no separate remove call is needed. Errors are
// ignored - the most common one is simply "no such container" when
// nothing is running yet.
func stopWatchedContainer(runtimeName, name string, execute containerExecutor) {
	_ = execute(runtimeName, []string{"stop", name}, nil, io.Discard, io.Discard)
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
		"api_version": cliOutputAPIVersion,
		"config":      filename, "version": project.CurrentConfigVersion,
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
		fmt.Fprintf(stderr, "  next: run `pf init` in your project directory, then retry `pf %s`\n", action)
		return 1
	}
	if result.Ambiguous {
		fmt.Fprintf(stderr,
			"detected multiple ecosystems (%s); write a %s with an explicit language to resolve the ambiguity\n",
			strings.Join(result.Candidates, ", "), project.ConfigNames[0])
		fmt.Fprintf(stderr, "  next: run `pf init --language <one of the above>`, then retry `pf %s`\n", action)
		return 2
	}
	fmt.Fprintf(stderr, `detected a %s project (evidence: %s); running pf init will generate a %s like:

version: 1
language: %s
artifact: RELATIVE/PATH/TO/EXECUTABLE-OR-ENTRY
`, result.Kind, strings.Join(result.Evidence, ", "), project.ConfigNames[0], result.Kind)
	fmt.Fprintf(stderr, "  next: run `pf init`, then retry `pf %s`\n", action)
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
	capabilities, capabilityErr := projectapp.AssessBuildCapabilities(context.Background(), loaded)
	result["capabilities"] = capabilities
	if capabilityErr != nil {
		result["valid"] = false
		result["blockers"] = []string{capabilityErr.Error()}
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
	if capabilityErr != nil {
		return 2
	}
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
	return freezeProjectWithBootPins(loaded, plugins, "", "", stdout, stderr, execute)
}

func freezeProjectWithBootPins(loaded project.Loaded, plugins *pluginHost, kernelPath, initramfsPath string, stdout, stderr io.Writer, execute projectExecutor) int {
	if (kernelPath == "") != (initramfsPath == "") {
		fmt.Fprintln(stderr, "platform-factory project freeze: --kernel and --initramfs must be provided together")
		return 2
	}
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
		fmt.Fprintf(stderr, "platform-factory: freeze: %s\n", shellquote.Command(step.args[0], step.args[1:]))
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
	lockPath := loaded.AdjacentLockPath()
	if lock, lockErr := project.LoadLock(lockPath); lockErr == nil && lock.Version == project.CurrentLockVersion {
		sources, toolchains, pinErr := loaded.FrozenInputPins()
		if pinErr != nil {
			fmt.Fprintf(stderr, "platform-factory project freeze: lock pins: %v\n", pinErr)
			return 1
		}
		lock.Sources, lock.Toolchains = sources, toolchains
		if kernelPath != "" {
			lock.Kernel, pinErr = func() (*project.LockedInput, error) {
				pin, err := loaded.PinLocalFile(filepath.ToSlash(kernelPath))
				return &pin, err
			}()
			if pinErr == nil {
				lock.Initramfs, pinErr = func() (*project.LockedInput, error) {
					pin, err := loaded.PinLocalFile(filepath.ToSlash(initramfsPath))
					return &pin, err
				}()
			}
			if pinErr != nil {
				fmt.Fprintf(stderr, "platform-factory project freeze: boot pins: %v\n", pinErr)
				return 1
			}
		}
		if pinErr := atomicfile.WriteJSONSensitive(lockPath, lock); pinErr != nil {
			fmt.Fprintf(stderr, "platform-factory project freeze: update lock: %v\n", pinErr)
			return 1
		}
	} else if lockErr != nil && !errors.Is(lockErr, os.ErrNotExist) {
		fmt.Fprintf(stderr, "platform-factory project freeze: load lock: %v\n", lockErr)
		return 1
	}
	result, _ := json.MarshalIndent(map[string]any{
		"api_version": cliOutputAPIVersion,
		"config":      loaded.File, "freeze_lock": inventory,
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
	if loaded.Config.DependencyManagement != nil {
		switch loaded.Config.DependencyManagement.Mode {
		case "none", "external":
			return nil, nil
		case "unresolved", "unknown":
			return nil, fmt.Errorf("dependency state is %s; resolve it before freezing", loaded.Config.DependencyManagement.Mode)
		}
	}
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
	return buildProjectContext(context.Background(), loaded, stdout, stderr, execute)
}

func buildProjectContext(ctx context.Context, loaded project.Loaded, stdout, stderr io.Writer, execute projectExecutor) (string, int) {
	return buildProjectContextWithBudget(ctx, loaded, stdout, stderr, execute, budget.Budget{})
}

func buildProjectContextWithBudget(ctx context.Context, loaded project.Loaded, stdout, stderr io.Writer, execute projectExecutor, resourceBudget budget.Budget) (string, int) {
	startedAt := time.Now()
	if err := loaded.VerifyAdjacentLock(); err != nil {
		fmt.Fprintf(stderr, "platform-factory project build: project lock preflight failed: %v\n", err)
		fmt.Fprintln(stderr, "  next: restore the manifest that matches the lock, or regenerate and review the v2 lock")
		return "", 2
	}
	buildPolicy, err := preflightProjectBuildPolicy(loaded)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project build: policy preflight failed: %v\n", err)
		return "", 2
	}
	capabilities, err := projectapp.AssessBuildCapabilities(ctx, loaded)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project build: capability preflight failed: %v\n", err)
		return "", 2
	}
	if err := projectapp.ValidateBuildDAG(loaded); err != nil {
		fmt.Fprintf(stderr, "platform-factory project build: invalid build DAG: %v\n", err)
		fmt.Fprintln(stderr, "  next: run `pf init --dry-run` to review the proposed DAG, then repair or regenerate .pf/build.pipeline.json")
		return "", 1
	}
	if projectapp.RequiresFrozenInputs(loaded) {
		if err := loaded.VerifyFreezeInventory(); err != nil {
			fmt.Fprintf(stderr, "platform-factory project build: frozen inputs are not valid: %v\n", err)
			fmt.Fprintln(stderr, "  next: run `pf freeze`, review the inventory, then retry `pf build`")
			return "", 1
		}
	}
	if len(loaded.Config.BuildCommand) > 0 {
		command := loaded.Config.BuildCommand
		fmt.Fprintf(stderr, "platform-factory: build: %s\n", shellquote.Command(command[0], command[1:]))
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
	projectFiles, err := loaded.ImageFiles()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project build: dependencies: %v\n", err)
		return "", 1
	}
	extraFiles := make([]oci.ExtraFile, len(projectFiles))
	for i, file := range projectFiles {
		extraFiles[i] = oci.ExtraFile{Dest: file.Dest, Source: file.Source, Mode: file.Mode, Category: file.Category}
	}
	primaryDestination := "/app/" + filepath.Base(binary)
	extraFiles = slices.DeleteFunc(extraFiles, func(file oci.ExtraFile) bool {
		source := filepath.ToSlash(file.Source)
		// .platform-factory/deps/<language>/runtime is where
		// `pf plugin provision-runtime` stages an interpreter and its
		// libraries. It is always already reachable through its own
		// explicit pf.yaml `runtime`/`include` destinations
		// (/runtime/..., or a real absolute image path like
		// /usr/local/lib/python3.12) - those entries must survive this
		// filter untouched. Only the *duplicate* copy the generic
		// project sweep also produces, landing under the sweep's own
		// /app/... destination prefix, is excluded here: without this,
		// the same content lands in the image twice at two different
		// paths, wasting space and - for a full interpreter standard
		// library - large enough to trip internal/layout's own
		// per-layer size budget on its own. Checking the destination
		// prefix (not just the source path) is what keeps this from
		// also matching - and wrongly dropping - the legitimate
		// explicit include entries, which share the same source tree
		// but never land under /app/.
		return file.Dest == primaryDestination || strings.Contains(source, "/.platform-factory/build/") ||
			strings.Contains(source, "/.platform-factory/artifacts/") ||
			(strings.HasPrefix(file.Dest, "/app/.platform-factory/deps/") && strings.Contains(file.Dest, "/runtime/"))
	})
	languageLayer, cleanupLanguageLayer, err := projectapp.LanguagePluginLayer(loaded, stderr, execute, resolveLoadedPlugin)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project build: %v\n", err)
		return "", 1
	}
	defer cleanupLanguageLayer()
	var extraLayers []string
	if languageLayer != "" {
		extraLayers = []string{languageLayer}
	}
	osName, architecture, err := buildapp.ParsePlatform(loaded.Config.Platform)
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
		profile = projectapp.Profile(loaded.Config.Language)
	}
	created := earliestProjectFileTime(loaded.Root)
	if created.IsZero() {
		created = time.Now()
	}
	digest, err := oci.Build(oci.Options{
		Binary: binary, Output: loaded.Output(), Architecture: architecture, OS: osName,
		Entrypoint: entrypoint, Profile: profile, ImageName: loaded.Config.Image,
		Tag: loaded.Config.Tag, Created: created.UTC(), Args: loaded.Config.Args,
		Env: loaded.Config.Env, User: loaded.Config.User, ExtraFiles: extraFiles,
		Compression: "fast", TraceID: observability.TraceIDFromContext(ctx),
		SemanticLayers: loaded.Config.SemanticLayers, ExtraLayers: extraLayers,
		Observer: cliObserver(stderr),
		Budget:   resourceBudget,
	})
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory project build: %v\n", err)
		return "", 1
	}
	bootBundlePath := ""
	if loaded.Config.Isolation == "microvm" {
		bootBundlePath, err = writeProjectBootBundle(loaded, digest)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory project build: write boot bundle: %v\n", err)
			return "", 1
		}
	}
	resultMap := map[string]any{
		"api_version": cliOutputAPIVersion,
		"config":      loaded.File, "digest": digest, "isolation": loaded.Config.Isolation,
		"layout": loaded.Output(), "reference": loaded.Config.Image + ":" + loaded.Config.Tag,
		"valid":        true,
		"capabilities": capabilities,
	}
	if bootBundlePath != "" {
		resultMap["boot_bundle"] = bootBundlePath
	}
	releaseDir := filepath.Join(loaded.Root, ".platform-factory", "release")
	reportsDir := filepath.Join(releaseDir, "reports")
	settings := buildapp.Settings{
		Entrypoint: entrypoint, Profile: profile, Image: loaded.Config.Image,
		Tag: loaded.Config.Tag, Created: time.Unix(0, 0), ExtraFiles: extraFiles,
	}
	target := buildapp.Target{OS: osName, Architecture: architecture, Input: binary}
	if err := buildapp.WriteSBOMToDist(releaseDir, target, settings); err != nil {
		fmt.Fprintf(stderr, "platform-factory project build: write release SBOM: %v\n", err)
		return "", 1
	}
	if err := atomicfile.WriteJSON(reportsDir, "build.json", resultMap); err != nil {
		fmt.Fprintf(stderr, "platform-factory project build: write build report: %v\n", err)
		return "", 1
	}
	if err := buildapp.WriteBuildEvidence(releaseDir, reportsDir, buildPolicy.keyDir, "release", version, resultMap, target, settings); err != nil {
		fmt.Fprintf(stderr, "platform-factory project build: write release evidence: %v\n", err)
		return "", 1
	}
	if err := persistProjectBuildPolicy(buildPolicy, releaseDir, reportsDir, digest); err != nil {
		fmt.Fprintf(stderr, "platform-factory project build: enforce configured build policy: %v\n", err)
		return "", 1
	}
	verified, verifyErr := layout.Verify(loaded.Output())
	if verifyErr != nil {
		fmt.Fprintf(stderr, "platform-factory project build: collect metrics: %v\n", verifyErr)
		return "", 1
	}
	if err := atomicfile.WriteJSON(reportsDir, "metrics.json", map[string]any{
		"api_version": "platform-factory.dev/metrics/v1", "operation": "build",
		"trace_id": observability.TraceIDFromContext(ctx), "duration_ms": time.Since(startedAt).Milliseconds(),
		"platforms": len(verified.Platforms), "manifests": verified.Manifests, "blobs": verified.Blobs, "success": true,
	}); err != nil {
		fmt.Fprintf(stderr, "platform-factory project build: write metrics: %v\n", err)
		return "", 1
	}
	result, _ := json.MarshalIndent(resultMap, "", "  ")
	fmt.Fprintln(stdout, string(result))
	return digest, 0
}

func writeProjectBootBundle(loaded project.Loaded, rootfsDigest string) (string, error) {
	lock, err := project.LoadLock(loaded.AdjacentLockPath())
	if errors.Is(err, os.ErrNotExist) {
		// Compatibility for hand-written projects predating lock v2. They may
		// still build, but cannot claim a content-addressed boot bundle.
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if lock.Version == 1 {
		return "", nil
	}
	if lock.Kernel == nil || lock.Initramfs == nil {
		return "", errors.New("microvm boot bundle requires a v2 lock with kernel and initramfs pins")
	}
	bundle, err := runtimeapp.NewBootBundle(lock.Kernel.Digest, lock.Initramfs.Digest, rootfsDigest, nil, map[string]string{
		"platform":  loaded.Config.Platform,
		"reference": loaded.Config.Image + ":" + loaded.Config.Tag,
	})
	if err != nil {
		return "", err
	}
	distDir := filepath.Join(loaded.Root, "dist")
	bundleDir := filepath.Join(distDir, "boot-bundle")
	for _, directory := range []string{distDir, bundleDir} {
		info, statErr := os.Lstat(directory)
		if errors.Is(statErr, os.ErrNotExist) {
			if err := os.Mkdir(directory, 0o755); err != nil {
				return "", err
			}
			continue
		}
		if statErr != nil {
			return "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("refusing unsafe boot bundle directory %s", directory)
		}
	}
	if err := atomicfile.WriteJSON(bundleDir, "bundle.json", bundle); err != nil {
		return "", err
	}
	return bundleDir, nil
}

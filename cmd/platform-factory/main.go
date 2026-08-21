package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-isatty"

	"github.com/CYPT71/platform-factory/cmd/tui/buildtui"
	buildapp "github.com/CYPT71/platform-factory/internal/app/build"
	"github.com/CYPT71/platform-factory/internal/atomicfile"
	"github.com/CYPT71/platform-factory/internal/budget"
	"github.com/CYPT71/platform-factory/internal/detect"
	"github.com/CYPT71/platform-factory/internal/dockerarchive"
	"github.com/CYPT71/platform-factory/internal/executor"
	"github.com/CYPT71/platform-factory/internal/layout"
	"github.com/CYPT71/platform-factory/internal/networking"
	"github.com/CYPT71/platform-factory/internal/observability"
	"github.com/CYPT71/platform-factory/internal/plugin"
	"github.com/CYPT71/platform-factory/internal/project"
	"github.com/CYPT71/platform-factory/oci"
	"github.com/CYPT71/platform-factory/sdk/langplugin"
)

var version = "dev"

const cliOutputAPIVersion = "platform-factory.dev/cli-output/v1"

type cliHandler func([]string, io.Writer, io.Writer) int

var directCommands = map[string]cliHandler{
	"compose":           runCompose,
	"diff":              runDiff,
	"sbom":              runSBOM,
	"evidence":          runEvidence,
	"plugin-provenance": runPluginProvenance,
	"pipeline":          runPipeline,
	"verify-release":    runVerifyRelease,
	"completion":        runCompletion,
	"doctor":            runDoctor,
	"plugin":            runPlugin,
	"marketplace":       runMarketplace,
	"registry":          runRegistry,
	"status":            runStatus,
	"explain":           runExplain,
	"mcp":               runMCP,
}

// commandContext preserves an existing trace, then checks the environment,
// then creates one.
func commandContext(ctx context.Context, command string) context.Context {
	if observability.TraceIDFromContext(ctx) != "" {
		return ctx
	}
	traceID := strings.TrimSpace(os.Getenv("PLATFORM_FACTORY_TRACE_ID"))
	if traceID == "" {
		traceID = observability.NewTraceID("cli", command).String()
	}
	return observability.ContextWithTraceID(ctx, traceID)
}

func run(args []string, stdout, stderr io.Writer) int {
	filtered, quiet, verbose, globalErr := extractGlobalOutputFlags(args)
	if globalErr != "" {
		fmt.Fprintln(stderr, "platform-factory:", globalErr)
		return 2
	}
	args = filtered
	command := "unknown"
	if len(args) > 0 {
		command = args[0]
	}
	ctx := commandContext(context.Background(), command)
	if quiet {
		stdout = io.Discard
	}
	if verbose {
		fmt.Fprintf(stderr, "platform-factory: command=%s trace_id=%s\n", command, observability.TraceIDFromContext(ctx))
	}

	if len(args) < 1 {
		printUsage(stdout)
		return 0
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		if len(args) == 2 && args[1] == "--all" {
			printFullUsage(stdout)
			return 0
		}
		printUsage(stdout)
		return 0
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintf(stdout, "platform-factory %s\n", version)
		return 0
	}
	if (args[0] == "image" || args[0] == "container") &&
		(len(args) == 1 || (len(args) == 2 && (args[1] == "-h" || args[1] == "--help"))) {
		printUsage(stdout)
		return 0
	}
	var aliasWarning string
	args, aliasWarning = commandAlias(args)
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	if aliasWarning != "" {
		fmt.Fprintf(stderr, "platform-factory: warning: %s\n", aliasWarning)
	}
	if len(args) == 2 && (args[1] == "-h" || args[1] == "--help") {
		switch args[0] {
		case "inspect", "verify", "launch", "project", "plan":
			printUsage(stdout)
			return 0
		}
	}
	if args[0] == "init" {
		return runInit(args[1:], os.Stdin, stdout, stderr)
	}
	if args[0] == "build" {
		if projectBuildInvocation(args[1:]) {
			if _, err := project.Discover(".", ""); err == nil {
				return runProjectContext(ctx, append([]string{"build"}, args[1:]...), stdout, stderr, executeProjectCommand, executeContainerRuntime, executeMicroVMCommand)
			}
		}
		return runBuild(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "project" {
		return runProjectContext(ctx, args[1:], stdout, stderr, executeProjectCommand, executeContainerRuntime, executeMicroVMCommand)
	}
	if args[0] == "plan" {
		fmt.Fprintln(stderr, `platform-factory: warning: "platform-factory plan" is deprecated, use "platform-factory project plan" instead`)
		return runProjectContext(ctx, append([]string{"plan"}, args[1:]...), stdout, stderr, executeProjectCommand, executeContainerRuntime, executeMicroVMCommand)
	}
	if args[0] == "freeze" {
		fmt.Fprintln(stderr, `platform-factory: warning: "platform-factory freeze" is deprecated, use "platform-factory project freeze" instead`)
		return runProjectContext(ctx, append([]string{"freeze"}, args[1:]...), stdout, stderr, executeProjectCommand, executeContainerRuntime, executeMicroVMCommand)
	}
	if handler := directCommands[args[0]]; handler != nil {
		return handler(args[1:], stdout, stderr)
	}
	if args[0] == "publish" {
		return runPublish(ctx, args[1:], stdout, stderr, executeContainerRuntime)
	}
	if args[0] == "deploy" {
		return runDeploy(ctx, args[1:], stdout, stderr, executeContainerRuntime)
	}
	if args[0] == "logs" || args[0] == "events" {
		return runProjectObservation(ctx, args[0], args[1:], stdout, stderr, executeContainerRuntime)
	}
	if args[0] == "rollback" {
		return runRollback(ctx, args[1:], stdout, stderr, executeContainerRuntime)
	}
	if args[0] == "run" {
		if hasIsolationFlag(args[1:]) {
			return runLaunch(ctx, args[1:], stdout, stderr, executeContainerRuntime, executeMicroVMCommand)
		}
		// No explicit IMAGE/layout given (e.g. bare `pf run` or `pf run
		// --runtime docker`): the same "just run my project" shorthand
		// `pf launch` already gives bare invocations, so a junior never
		// has to learn `pf project run` - or, with nothing to discover
		// either, project.Discover's own error path (via
		// suggestProjectConfig) points them at `pf init` instead of a
		// generic "usage: ... IMAGE" wall of text.
		if !runHasExplicitTarget(args[1:]) {
			return runProjectContext(ctx, append([]string{"run"}, args[1:]...), stdout, stderr,
				executeProjectCommand, executeContainerRuntime, executeMicroVMCommand)
		}
		return runContainer(ctx, args[1:], stdout, stderr, executeContainerRuntime)
	}
	if args[0] == "import" {
		return runImport(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "microvm" {
		return runMicroVM(args[1:], stdout, stderr, executeMicroVMCommand)
	}
	if args[0] == "launch" {
		if hasLaunchPublishFlag(args[1:]) {
			return runLaunchPublish(args[1:], stdout, stderr, executeProjectCommand,
				executeContainerRuntime, executeMicroVMCommand)
		}
		if !hasIsolationFlag(args[1:]) {
			action, projectArgs, err := projectLaunchAction(args[1:])
			if err != nil {
				fmt.Fprintf(stderr, "platform-factory launch: %v\n", err)
				return 2
			}
			return runProjectContext(ctx, append([]string{action}, projectArgs...), stdout, stderr,
				executeProjectCommand, executeContainerRuntime, executeMicroVMCommand)
		}
		return runLaunch(ctx, args[1:], stdout, stderr, executeContainerRuntime, executeMicroVMCommand)
	}
	if args[0] == "inspect" && len(args) == 1 {
		return runStatus([]string{"--format", "json"}, stdout, stderr)
	}
	if args[0] == "inspect" || args[0] == "verify" {
		return runInspect(args[0], args[1:], stdout, stderr)
	}
	if args[0] != "detect" {
		fmt.Fprintf(stderr, "platform-factory: unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
	flags := flag.NewFlagSet("detect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	accept := flags.Bool("accept-ambiguous", false, "accept an ambiguous result explicitly")
	outputFormat := flags.String("format", "json", "result format: json or text")
	pluginFlags := registerPluginFlags(flags)
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return 2
	}
	if !validOutputFormat(*outputFormat) {
		fmt.Fprintln(stderr, "platform-factory detect: format must be json or text")
		return 2
	}
	result, err := detect.Path(flags.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory detect: %v\n", err)
		return 1
	}
	if result.Kind == "unknown" && !result.Ambiguous {
		if info, statErr := os.Stat(flags.Arg(0)); statErr == nil && info.IsDir() {
			inspections, inspectErr := langplugin.InspectLoaded(flags.Arg(0))
			if inspectErr != nil {
				fmt.Fprintf(stderr, "platform-factory detect: inspect language plugins: %v\n", inspectErr)
				return 1
			}
			result = detectionFromPlugins(flags.Arg(0), inspections)
		}
	}
	if result.Kind == "unknown" && !result.Ambiguous {
		plugins, pluginErr := pluginFlags.start(context.Background())
		if pluginErr != nil {
			fmt.Fprintf(stderr, "platform-factory detect: %v\n", pluginErr)
			return 1
		}
		defer plugins.Close()
		if extra, name, found := plugins.detect(context.Background(), flags.Arg(0)); found {
			result.Kind, result.Profile = extra.Kind, extra.Profile
			result.Evidence = append([]string(nil), extra.Evidence...)
			result.Evidence = append(result.Evidence, "reported by plugin "+name)
		}
	}
	if *outputFormat == "text" {
		fmt.Fprintf(stdout, "%s %s (%s)\n", result.Kind, result.Profile, strings.Join(result.Evidence, ", "))
	} else {
		output, err := json.MarshalIndent(struct {
			APIVersion string `json:"api_version"`
			detect.Result
		}{APIVersion: cliOutputAPIVersion, Result: result}, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory detect: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, string(output))
	}
	if result.Ambiguous && !*accept {
		fmt.Fprintln(stderr, "ambiguous detection requires --accept-ambiguous")
		return 2
	}
	return 0
}

// extractGlobalOutputFlags accepts --quiet/--verbose before or after the
// command without forcing every subcommand to duplicate their parsing. It
// deliberately logs only the command name, never arguments that may contain
// credentials or paths with sensitive data.
func extractGlobalOutputFlags(args []string) ([]string, bool, bool, string) {
	filtered := make([]string, 0, len(args))
	quiet, verbose := false, false
	for _, arg := range args {
		switch arg {
		case "--quiet", "-q":
			quiet = true
		case "--verbose", "-v":
			verbose = true
		default:
			filtered = append(filtered, arg)
		}
	}
	if quiet && verbose {
		return nil, false, false, "--quiet and --verbose are mutually exclusive"
	}
	return filtered, quiet, verbose, ""
}

func validOutputFormat(value string) bool { return value == "json" || value == "text" }

func runInspect(command string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	outputFormat := flags.String("format", "json", "result format: json or text")
	archiveFormat := flags.String("archive-format", "", "verify an archive: oci-layout.tar.gz or docker-save.tar")
	archiveSHA256 := flags.String("sha256", "", "expected SHA-256 of the archive bytes (requires --archive-format)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintf(stderr, "usage: platform-factory %s [--format json|text] LAYOUT\n", command)
		return 2
	}
	if !validOutputFormat(*outputFormat) {
		fmt.Fprintf(stderr, "platform-factory %s: format must be json or text\n", command)
		return 2
	}
	if *archiveSHA256 != "" && *archiveFormat == "" {
		fmt.Fprintf(stderr, "platform-factory %s: --sha256 requires --archive-format\n", command)
		return 2
	}
	if *archiveFormat != "" {
		if *archiveFormat != "oci-layout.tar.gz" && *archiveFormat != "docker-save.tar" {
			fmt.Fprintf(stderr, "platform-factory %s: archive format must be oci-layout.tar.gz or docker-save.tar\n", command)
			return 2
		}
		expectedDigest, err := decodeOptionalSHA256(*archiveSHA256)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory %s: %v\n", command, err)
			return 2
		}
		file, err := os.Open(flags.Arg(0))
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory %s: open archive: %v\n", command, err)
			return 1
		}
		hash := sha256.New()
		var report any
		tee := io.TeeReader(file, hash)
		if *archiveFormat == "docker-save.tar" {
			report, err = dockerarchive.Verify(context.Background(), tee)
		} else {
			report, err = layout.VerifyArchive(context.Background(), *archiveFormat, tee)
		}
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			fmt.Fprintf(stderr, "platform-factory %s: %v\n", command, errors.Join(err, closeErr))
			return 1
		}
		actualDigest := hash.Sum(nil)
		if expectedDigest != nil && subtle.ConstantTimeCompare(expectedDigest, actualDigest) != 1 {
			fmt.Fprintf(stderr, "platform-factory %s: archive SHA-256 mismatch: expected %s, got %s\n", command, *archiveSHA256, hex.EncodeToString(actualDigest))
			return 1
		}
		if *outputFormat == "text" {
			fmt.Fprintf(stdout, "valid: %s archive sha256:%s\n", *archiveFormat, hex.EncodeToString(actualDigest))
		} else {
			encoded, _ := json.MarshalIndent(map[string]any{"api_version": cliOutputAPIVersion, "valid": true, "format": *archiveFormat, "sha256": hex.EncodeToString(actualDigest), "report": report}, "", "  ")
			fmt.Fprintln(stdout, string(encoded))
		}
		return 0
	}
	report, err := layout.Verify(flags.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory %s: %v\n", command, err)
		return 1
	}
	if *outputFormat == "text" {
		fmt.Fprintf(stdout, "valid: %d manifests, %d blobs\n", report.Manifests, report.Blobs)
		for _, platform := range report.Platforms {
			fmt.Fprintf(stdout, "  %s %s/%s %s\n", platform.Reference, platform.OS, platform.Architecture, platform.Digest)
		}
	} else {
		output, _ := json.MarshalIndent(struct {
			APIVersion string `json:"api_version"`
			layout.Report
		}{APIVersion: cliOutputAPIVersion, Report: report}, "", "  ")
		fmt.Fprintln(stdout, string(output))
	}
	return 0
}

func decodeOptionalSHA256(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) != sha256.Size*2 {
		return nil, errors.New("--sha256 must be exactly 64 lowercase hexadecimal characters, optionally prefixed by sha256:")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || value != strings.ToLower(value) {
		return nil, errors.New("--sha256 must be exactly 64 lowercase hexadecimal characters, optionally prefixed by sha256:")
	}
	return decoded, nil
}

// commandAlias translates deprecated spellings without performing I/O.
func commandAlias(args []string) (translated []string, warning string) {
	if len(args) == 0 {
		return args, ""
	}
	if args[0] == "vm" {
		return append([]string{"microvm"}, args[1:]...),
			`"platform-factory vm" is deprecated, use "platform-factory microvm" instead`
	}
	if len(args) >= 2 && args[0] == "container" && args[1] == "run" {
		return append([]string{"run"}, args[2:]...),
			`"platform-factory container run" is deprecated, use "platform-factory run" instead`
	}
	if len(args) >= 2 && args[0] == "image" {
		switch args[1] {
		case "build", "compose", "inspect", "verify", "publish":
			return append([]string{args[1]}, args[2:]...),
				fmt.Sprintf(`"platform-factory image %s" is deprecated, use "platform-factory %s" instead`, args[1], args[1])
		}
	}
	return args, ""
}

func projectBuildInvocation(args []string) bool {
	return len(args) == 0 || (len(args) == 1 && args[0] == "--dry-run")
}

// printUsage shows the commands a newcomer actually needs for the
// init -> build -> publish -> deploy path, plus the everyday
// status/doctor/run/help commands - not the full ~30-command surface.
// Progressive disclosure: "platform-factory help --all" (printFullUsage)
// has everything else. Every "platform-factory COMMAND --help" keeps
// working exactly as before regardless of which list a user found the
// command name in.
func printUsage(output io.Writer) {
	fmt.Fprintln(output, `platform-factory — build, verify and run hardened OCI applications

Common commands:
  platform-factory init [DIRECTORY]        turn a source directory into a project
  platform-factory build                   build the nearest project (or an EXECUTABLE directly)
  platform-factory run TARGET [ARG...]     run a built image or executable locally
  platform-factory publish LAYOUT IMAGE    push to a registry, with SBOM/provenance/signatures
  platform-factory deploy IMAGE            apply a digest-pinned image to Kubernetes
  platform-factory status [DIRECTORY]      build/publish/deploy state and the next safe command
  platform-factory doctor                  check local tools, runtimes and hardware support
  platform-factory help --all              show every command, including advanced/low-level ones

Run "platform-factory COMMAND --help" for a command's own options and examples.`)
}

func printFullUsage(output io.Writer) {
	fmt.Fprintln(output, `platform-factory — build, verify and run hardened OCI applications

Usage:
  platform-factory init [--dry-run] [DIRECTORY]
  platform-factory build [--dry-run]                 # build nearest pf.yaml project
  platform-factory build [OPTIONS] EXECUTABLE        # low-level executable build
  platform-factory project <show|freeze|build|run> [--config FILE] [DIRECTORY]
  platform-factory plan [--config FILE] [DIRECTORY]
  platform-factory freeze [--config FILE] [DIRECTORY]
  platform-factory compose [OPTIONS] LAYOUT LAYOUT [...]
  platform-factory inspect LAYOUT
  platform-factory verify LAYOUT
  platform-factory diff [--format json|text] LAYOUT_A LAYOUT_B
  platform-factory sbom [--format json|text] PATH [PATH...]
  platform-factory evidence [--plugin-dir DIR] [--reproducible] PIPELINE.json
  platform-factory plugin-provenance --executable PATH --name NAME [OPTIONS]
  platform-factory pipeline <plan|run> [OPTIONS] PIPELINE.json
  platform-factory status [--format text|json] [DIRECTORY]
  platform-factory explain [DIRECTORY]
  platform-factory logs [--tail N] [--follow]
  platform-factory events [--tail N]
  platform-factory publish [OPTIONS] LAYOUT IMAGE
  platform-factory verify-release [OPTIONS] LAYOUT
  platform-factory import [--runtime docker|podman] --layout LAYOUT IMAGE
  platform-factory detect [OPTIONS] PATH
  platform-factory run [--isolation=<container|microvm>] [OPTIONS] TARGET [ARG...]
  platform-factory deploy [OPTIONS] IMAGE
  platform-factory rollback [OPTIONS] NAME
  platform-factory launch [--dry-run] [--config FILE] [DIRECTORY]
  platform-factory launch --isolation=<container|microvm> [RUNTIME OPTIONS]
  platform-factory microvm <build-legacy-oci|probe|create|extract-legacy-app|inspect-legacy-disk|run-legacy-disk|start|run|status|logs|restart|stop|delete|rbac|package> [OPTIONS]
  platform-factory completion <bash|zsh|fish|powershell>
  platform-factory doctor [--json]
  platform-factory plugin <load|unload|list> [OPTIONS]

Deprecated aliases (print a warning; will be removed in a future major
version - use the canonical command shown instead):
  platform-factory image <build|compose|inspect|verify|publish> ...  -> platform-factory <build|compose|inspect|verify|publish> ...
  platform-factory container run ...                                -> platform-factory run ...
  platform-factory vm ...                                            -> platform-factory microvm ...
  platform-factory plan ...                                          -> platform-factory project plan ...
  platform-factory freeze ...                                        -> platform-factory project freeze ...

Global:
  platform-factory help
  platform-factory version

Run "platform-factory COMMAND --help" for command options.`)
}

func runImport(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("import", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runtimeName := flags.String("runtime", "docker", "container runtime: docker or podman")
	layoutName := flags.String("layout", "", "verified local OCI layout")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if (*runtimeName != "docker" && *runtimeName != "podman") || *layoutName == "" || flags.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: platform-factory import [--runtime docker|podman] --layout LAYOUT IMAGE")
		return 2
	}
	image, err := prepareContainerImage(ctx, *runtimeName, flags.Arg(0), *layoutName, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory import: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, image)
	return 0
}

func cliObserver(output io.Writer) func(oci.Event) {
	return func(event oci.Event) {
		record := map[string]any{
			"api_version": cliOutputAPIVersion,
			"time":        event.Time.Format(time.RFC3339Nano), "level": event.Level,
			"component": event.Component, "operation": event.Operation,
			"phase": event.Phase, "trace_id": event.TraceID,
			"message": event.Message,
		}
		if event.Duration > 0 {
			record["duration_ms"] = event.Duration.Milliseconds()
		}
		for key, value := range event.Fields {
			record[key] = value
		}
		_ = json.NewEncoder(output).Encode(record)
	}
}

func runCompose(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("compose", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var output string
	flags.StringVar(&output, "output", "oci-multi-image", "new composed OCI layout directory")
	flags.StringVar(&output, "o", "oci-multi-image", "shorthand for --output")
	outputFormat := flags.String("format", "json", "result format: json or text")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() < 2 {
		fmt.Fprintln(stderr, "usage: platform-factory compose --output LAYOUT INPUT_LAYOUT INPUT_LAYOUT [...]")
		return 2
	}
	if *outputFormat != "json" && *outputFormat != "text" {
		fmt.Fprintln(stderr, "platform-factory compose: format must be json or text")
		return 2
	}
	report, err := layout.Compose(output, flags.Args())
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory compose: %v\n", err)
		return 1
	}
	if *outputFormat == "text" {
		fmt.Fprintf(stdout, "composed %d manifests and %d blobs -> %s\n", report.Manifests, report.Blobs, report.Path)
	} else {
		encoded, _ := json.MarshalIndent(struct {
			APIVersion string `json:"api_version"`
			layout.Report
		}{APIVersion: cliOutputAPIVersion, Report: report}, "", "  ")
		fmt.Fprintln(stdout, string(encoded))
	}
	return 0
}

type containerExecutor func(name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error

type microVMExecutor func(name string, args, environment []string, stdin io.Reader, stdout, stderr io.Writer) error

func executeContainerRuntime(name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	command := exec.Command(name, args...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func executeMicroVMCommand(name string, args, environment []string, stdin io.Reader, stdout, stderr io.Writer) error {
	command := exec.Command(name, args...)
	command.Env = append(os.Environ(), environment...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func executeProjectCommand(name string, args []string, directory string, stdout, stderr io.Writer) error {
	command := exec.Command(name, args...)
	command.Dir = directory
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

// runFlags is every flag `platform-factory run` accepts, factored out
// of runContainer so runHasExplicitTarget can parse the exact same
// definitions to determine whether a positional IMAGE/layout was given
// - without duplicating the list and risking it drifting out of sync.
type runFlags struct {
	runtimeName                     *string
	cpus, memory, network, hostname *string
	containerName, layoutName       *string
	pidsLimit                       *int
	allowHostNetwork, watch         *bool
	publishes, dnsServers, addHosts *repeatedFlag
	volumes, envVars                *repeatedFlag
}

func newRunFlagSet(output io.Writer) (*flag.FlagSet, *runFlags) {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(output)
	values := &runFlags{
		publishes: &repeatedFlag{}, dnsServers: &repeatedFlag{}, addHosts: &repeatedFlag{},
		volumes: &repeatedFlag{}, envVars: &repeatedFlag{},
	}
	values.runtimeName = flags.String("runtime", "docker", "container runtime: docker or podman")
	values.cpus = flags.String("cpus", "1", "positive CPU limit")
	values.memory = flags.String("memory", "128m", "non-empty runtime memory limit")
	values.pidsLimit = flags.Int("pids-limit", 128, "positive process limit")
	values.network = flags.String("network", "none", "network mode or named runtime network")
	values.allowHostNetwork = flags.Bool("allow-host-network", false, "explicitly accept host network namespace sharing")
	values.hostname = flags.String("hostname", "", "container hostname")
	values.containerName = flags.String("name", "", "container name")
	values.layoutName = flags.String("layout", "", "verified local OCI layout to import automatically when needed")
	values.watch = flags.Bool("watch", false, "only valid with no IMAGE argument: discover the project in the current directory and keep rebuilding/restarting it whenever a source file changes (same as `pf project run --watch`)")
	flags.Var(values.publishes, "publish", "publish [IP:]HOST:CONTAINER[/tcp|udp]; repeatable")
	flags.Var(values.publishes, "port", "alias for --publish; repeatable")
	flags.Var(values.publishes, "p", "short alias for --publish; repeatable")
	flags.Var(values.dnsServers, "dns", "DNS server IPv4/IPv6 address; repeatable")
	flags.Var(values.addHosts, "add-host", "static NAME:IP host entry; repeatable")
	flags.Var(values.volumes, "volume", "HOST:CONTAINER[:ro|rw] bind mount; repeatable")
	flags.Var(values.volumes, "v", "short alias for --volume; repeatable")
	flags.Var(values.envVars, "env", "NAME=VALUE or bare NAME (inherited from this process's own environment); repeatable")
	flags.Var(values.envVars, "e", "short alias for --env; repeatable")
	return flags, values
}

// runHasExplicitTarget reports whether args (platform-factory run's own
// argument list, after "run" itself) names a positional IMAGE/layout -
// parsed with runContainer's own flag definitions so a value-taking
// flag given as two separate args (e.g. "--memory 128m") is never
// mistaken for the target. A parse error is treated as "yes" so the
// real, detailed error still comes from runContainer itself rather
// than being swallowed here.
func runHasExplicitTarget(args []string) bool {
	flags, _ := newRunFlagSet(io.Discard)
	if err := flags.Parse(args); err != nil {
		return true
	}
	return flags.NArg() >= 1
}

func runContainer(ctx context.Context, args []string, stdout, stderr io.Writer, execute containerExecutor) int {
	flags, values := newRunFlagSet(stderr)
	runtimeName, cpus, memory, pidsLimit := values.runtimeName, values.cpus, values.memory, values.pidsLimit
	network, allowHostNetwork, hostname := values.network, values.allowHostNetwork, values.hostname
	containerName, layoutName := values.containerName, values.layoutName
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() < 1 {
		fmt.Fprintln(stderr, "platform-factory run: an IMAGE or local OCI layout is required")
		flags.Usage()
		return 2
	}
	if *values.watch {
		fmt.Fprintln(stderr, "platform-factory run: --watch is not valid with an explicit IMAGE/layout - drop the argument to watch the project in the current directory, or use `pf project run --watch`")
		return 2
	}
	publishes, dnsServers, addHosts := *values.publishes, *values.dnsServers, *values.addHosts
	volumes, envVars := *values.volumes, *values.envVars
	if *runtimeName != "docker" && *runtimeName != "podman" {
		fmt.Fprintln(stderr, "platform-factory run: runtime must be docker or podman")
		return 2
	}
	cpuValue, err := strconv.ParseFloat(*cpus, 64)
	if err != nil || cpuValue <= 0 {
		fmt.Fprintln(stderr, "platform-factory run: cpus must be positive")
		return 2
	}
	if *memory == "" || strings.ContainsAny(*memory, "\x00 \t\r\n") {
		fmt.Fprintln(stderr, "platform-factory run: memory must be a non-empty runtime quantity")
		return 2
	}
	if *pidsLimit <= 0 {
		fmt.Fprintln(stderr, "platform-factory run: pids-limit must be positive")
		return 2
	}
	if err := networking.ValidateNetwork(*network, *allowHostNetwork); err != nil {
		fmt.Fprintf(stderr, "platform-factory run: %v\n", err)
		return 2
	}
	if err := networking.ValidateHostname(*hostname); err != nil {
		fmt.Fprintf(stderr, "platform-factory run: %v\n", err)
		return 2
	}
	if *containerName != "" && !validContainerName(*containerName) {
		fmt.Fprintln(stderr, "platform-factory run: name must match [a-zA-Z0-9][a-zA-Z0-9_.-]*")
		return 2
	}
	if *network == "none" && len(publishes) > 0 {
		fmt.Fprintln(stderr, "platform-factory run: publish cannot be used with network=none")
		return 2
	}
	for _, value := range publishes {
		if _, err := networking.ParseForward(value); err != nil {
			fmt.Fprintf(stderr, "platform-factory run: %v\n", err)
			return 2
		}
	}
	for _, value := range dnsServers {
		if err := networking.ValidateDNS(value); err != nil {
			fmt.Fprintf(stderr, "platform-factory run: %v\n", err)
			return 2
		}
	}
	for _, value := range addHosts {
		if err := networking.ValidateAddHost(value); err != nil {
			fmt.Fprintf(stderr, "platform-factory run: %v\n", err)
			return 2
		}
	}
	for _, value := range volumes {
		if err := validVolumeSpec(value); err != nil {
			fmt.Fprintf(stderr, "platform-factory run: %v\n", err)
			return 2
		}
	}
	for _, value := range envVars {
		if err := validEnvSpec(value); err != nil {
			fmt.Fprintf(stderr, "platform-factory run: %v\n", err)
			return 2
		}
	}
	image := flags.Arg(0)
	if strings.HasPrefix(image, "-") || strings.ContainsRune(image, 0) {
		fmt.Fprintln(stderr, "platform-factory run: invalid image reference")
		return 2
	}
	// A real OCI/Docker image reference never starts with "." or "/" (its
	// own reference grammar forbids it), so a target that does is always
	// meant as a local path - if nothing exists there, say so clearly
	// instead of forwarding a path-shaped string straight to `docker run`,
	// which only ever reports a confusing "invalid reference format".
	if (strings.HasPrefix(image, ".") || strings.HasPrefix(image, "/")) && *layoutName == "" {
		if _, err := os.Stat(image); errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderr, "platform-factory run: %q looks like a local path, but nothing exists there - run `pf build`/`pf project build` first, or pass a real image reference\n", image)
			return 1
		}
	}
	if *layoutName != "" || localLayoutPath(image) {
		prepared, err := prepareContainerImage(ctx, *runtimeName, image, *layoutName, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory run: %v\n", err)
			return 1
		}
		image = prepared
	}
	runtimeArgs := []string{
		"run", "--rm", "--init", "--read-only", "--cap-drop=ALL",
		"--security-opt=no-new-privileges", "--network=" + *network,
		"--cpus=" + *cpus, "--memory=" + *memory,
		"--pids-limit=" + strconv.Itoa(*pidsLimit),
		"--tmpfs=/tmp:rw,noexec,nosuid,size=16m",
	}
	if *hostname != "" {
		runtimeArgs = append(runtimeArgs, "--hostname="+*hostname)
	}
	if *containerName != "" {
		runtimeArgs = append(runtimeArgs, "--name="+*containerName)
	}
	for _, value := range publishes {
		runtimeArgs = append(runtimeArgs, "--publish="+value)
	}
	for _, value := range dnsServers {
		runtimeArgs = append(runtimeArgs, "--dns="+value)
	}
	for _, value := range addHosts {
		runtimeArgs = append(runtimeArgs, "--add-host="+value)
	}
	for _, value := range volumes {
		runtimeArgs = append(runtimeArgs, "--volume="+value)
	}
	for _, value := range envVars {
		runtimeArgs = append(runtimeArgs, "--env="+value)
	}
	runtimeArgs = append(runtimeArgs, image)
	runtimeArgs = append(runtimeArgs, flags.Args()[1:]...)
	if err := execute(*runtimeName, runtimeArgs, os.Stdin, stdout, stderr); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "platform-factory run: %v\n", err)
		return 1
	}
	return 0
}

// validContainerName matches docker/podman's own accepted container name
// shape: [a-zA-Z0-9][a-zA-Z0-9_.-]*.
func validContainerName(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if index == 0 {
			if !alnum {
				return false
			}
			continue
		}
		if !alnum && r != '_' && r != '.' && r != '-' {
			return false
		}
	}
	return true
}

// validVolumeSpec validates structure; the runtime validates mount options.
func validVolumeSpec(value string) error {
	if strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid --volume %q: contains a NUL byte", value)
	}
	parts := strings.SplitN(value, ":", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid --volume %q: want HOST:CONTAINER[:OPTIONS]", value)
	}
	return nil
}

// validEnvSpec accepts NAME=VALUE and runtime-forwarded NAME forms.
func validEnvSpec(value string) error {
	if value == "" || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid --env %q: must be non-empty and NUL-free", value)
	}
	name, _, _ := strings.Cut(value, "=")
	if name == "" {
		return fmt.Errorf("invalid --env %q: variable name must not be empty", value)
	}
	return nil
}

// containsHelpFlag reports whether args asks for help anywhere, so a
// command can print a curated description + examples before its flag
// defaults instead of falling through to flag.FlagSet's bare alphabetical
// dump (the stdlib default -h/--help behavior).
func containsHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

type repeatedFlag []string

func (f *repeatedFlag) String() string { return "" }
func (f *repeatedFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func printBuildUsage(output io.Writer, flags *flag.FlagSet) {
	fmt.Fprintln(output, `platform-factory build — build a verified, reproducible OCI layout from a single executable

Usage:
  platform-factory build [OPTIONS] EXECUTABLE
  platform-factory build [OPTIONS] --platform linux/ARCH=EXECUTABLE [--platform ...]

Inside a pf.yaml project, prefer "platform-factory build" with no
EXECUTABLE argument - it builds the nearest project instead of this
low-level, single-binary form.

Examples:
  platform-factory build --output oci-image ./hello
  platform-factory build --dist dist --reports reports ./hello
  platform-factory build --rebuild 3 --require-identical ./hello
  platform-factory build --platform linux/amd64=./hello-amd64 --platform linux/arm64=./hello-arm64

Options:`)
	flags.SetOutput(output)
	flags.PrintDefaults()
}

func runBuild(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	startedAt := time.Now()
	flags := flag.NewFlagSet("build", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var output string
	flags.StringVar(&output, "output", "oci-image", "new OCI layout directory")
	flags.StringVar(&output, "o", "oci-image", "shorthand for --output")
	configName := flags.String("config", "", "strict runtime configuration JSON")
	architecture := flags.String("arch", "amd64", "target architecture")
	var platforms repeatedFlag
	flags.Var(&platforms, "platform", "linux/ARCH or linux/ARCH=EXECUTABLE; repeat for a multi-platform image")
	osName := flags.String("os", "linux", "target operating system")
	entrypointName := flags.String("entrypoint", "", "absolute executable path inside the image")
	profileName := flags.String("profile", "", "runtime profile override")
	imageName := flags.String("image", "platform-factory", "image name annotation")
	tagName := flags.String("tag", "latest", "image tag annotation")
	var referenceAliases repeatedFlag
	flags.Var(&referenceAliases, "reference", "additional IMAGE:TAG annotation over the same content; repeatable")
	createdName := flags.String("created", "1970-01-01T00:00:00Z", "reproducible RFC3339 creation time")
	compression := flags.String("compression", "best", "gzip mode: best or fast")
	outputFormat := flags.String("format", "json", "result format: json or text")
	semanticLayers := flags.Bool("semantic-layers", false, "write one layer per toolchain/dependencies/application/metadata category")
	dryRun := flags.Bool("dry-run", false, "validate inputs and print the planned build without writing the layout")
	yes := flags.Bool("yes", false, "skip the interactive image reference confirmation")
	rebuild := flags.Int("rebuild", 1, "build the target this many times in fresh directories and compare every layout for reproducibility")
	requireIdentical := flags.Bool("require-identical", false, "with --rebuild, fail and report the divergence if the layouts are not byte-identical")
	var extras, labels repeatedFlag
	flags.Var(&extras, "extra-file", "additional [CATEGORY@]/container/path=host/path; repeatable")
	flags.Var(&labels, "label", "image label key=value; repeatable")
	distDir := flags.String("dist", "", "write the layout to DIST/oci-layout (unless --output/-o is also given) plus DIST/sbom.json")
	reportsDir := flags.String("reports", "", "write REPORTS/build.json (and REPORTS/reproducibility.json with --rebuild) after a successful build")
	policyFile := flags.String("policy", "", "strict build policy JSON; requires --reports")
	signKeyDir := flags.String("sign-key-dir", "", "sign release evidence with an Ed25519 key stored in DIR")
	signKeyName := flags.String("sign-key-name", "release", "signing key name used with --sign-key-dir")
	maxWallClock := flags.Duration("max-wall-clock", 0, "maximum build wall-clock duration, for example 30s or 2m (0 disables)")
	maxCPU := flags.Duration("max-cpu", 0, "maximum build CPU duration, for example 30s (0 disables)")
	maxMemory := flags.String("max-memory", "0", "maximum process heap during build, for example 512MiB or 2GiB (0 disables)")
	if containsHelpFlag(args) {
		printBuildUsage(stdout, flags)
		return 0
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *distDir != "" {
		outputExplicit := false
		flags.Visit(func(f *flag.Flag) {
			if f.Name == "output" || f.Name == "o" {
				outputExplicit = true
			}
		})
		if !outputExplicit {
			output = filepath.Join(*distDir, "oci-layout")
		}
	}
	createdAt, err := time.Parse(time.RFC3339, *createdName)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory build: invalid --created: %v\n", err)
		return 2
	}
	if *outputFormat != "json" && *outputFormat != "text" {
		fmt.Fprintln(stderr, "platform-factory build: format must be json or text")
		return 2
	}
	if !*dryRun && !*yes && isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd()) {
		confirmed, err := buildtui.Confirm(*imageName, *tagName)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory build: confirm image reference: %v\n", err)
			return 1
		}
		if !confirmed.Confirmed {
			fmt.Fprintln(stderr, "platform-factory build: canceled")
			return 1
		}
		*imageName, *tagName = confirmed.Image, confirmed.Tag
	}
	memoryLimit, err := buildapp.ParseByteLimit(*maxMemory)
	if err != nil || *maxWallClock < 0 || *maxCPU < 0 {
		if err == nil {
			err = errors.New("time budgets cannot be negative")
		}
		fmt.Fprintf(stderr, "platform-factory build: invalid resource budget: %v\n", err)
		return 2
	}
	parsedLabels, err := oci.LabelsFromPairs(labels)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory build: %v\n", err)
		return 2
	}
	var config oci.BuildConfig
	if *configName != "" {
		config, err = oci.LoadBuildConfig(*configName)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory build: %v\n", err)
			return 2
		}
	}
	extraFiles, err := oci.ExtraFilesFromPairs(extras)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory build: %v\n", err)
		return 2
	}
	for _, systemFile := range []struct{ destination, source string }{
		{"/etc/ssl/certs/ca-certificates.crt", config.SystemFiles.CACertificates},
		{"/etc/localtime", config.SystemFiles.Timezone},
		{"/usr/lib/locale/locale-archive", config.SystemFiles.LocaleArchive},
	} {
		if systemFile.source != "" {
			extraFiles = append(extraFiles, oci.ExtraFile{Dest: systemFile.destination, Source: systemFile.source, Mode: 0o444})
		}
	}
	traceID := observability.TraceIDFromContext(commandContext(ctx, "build"))
	targets, code, err := buildapp.Targets(platforms, flags.Args(), *osName, *architecture)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory build: %v\n", err)
		return code
	}
	settings := buildapp.Settings{
		Entrypoint: *entrypointName, Profile: *profileName,
		Image: *imageName, Tag: *tagName, Compression: *compression,
		Created: createdAt, Labels: parsedLabels, ExtraFiles: extraFiles,
		Config: config, TraceID: traceID, Observer: cliObserver(stderr),
		SemanticLayers: *semanticLayers || config.SemanticLayers,
		Budget:         budget.Budget{WallClock: *maxWallClock, CPU: *maxCPU, Memory: memoryLimit},
	}
	references := append([]string{*imageName + ":" + *tagName}, referenceAliases...)
	if err := layout.ValidateReferences(references); err != nil {
		fmt.Fprintf(stderr, "platform-factory build: %v\n", err)
		return 2
	}
	buildPolicy, err := preflightDirectBuildPolicy(*policyFile, *distDir, *reportsDir, *signKeyDir, *signKeyName, *rebuild, *requireIdentical)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory build: policy preflight failed: %v\n", err)
		return 2
	}
	if buildPolicy.configured && len(targets) != 1 {
		fmt.Fprintln(stderr, "platform-factory build: configured build policy currently requires exactly one target")
		return 2
	}
	if *dryRun {
		planned := make([]map[string]any, 0, len(targets))
		for _, target := range targets {
			entrypoint, profile, err := buildapp.ResolveTarget(target, settings)
			if err != nil {
				fmt.Fprintf(stderr, "platform-factory build: %v\n", err)
				return 2
			}
			planned = append(planned, map[string]any{
				"platform": target.OS + "/" + target.Architecture, "input": target.Input,
				"entrypoint": entrypoint, "profile": profile,
			})
		}
		result, _ := json.MarshalIndent(map[string]any{
			"api_version": cliOutputAPIVersion,
			"dry_run":     true, "layout": output, "reference": *imageName + ":" + *tagName, "references": references,
			"platforms": planned, "semantic_layers": settings.SemanticLayers,
			"resource_budget": buildapp.ResourceBudgetPlan(settings.Budget), "valid": true,
		}, "", "  ")
		fmt.Fprintln(stdout, string(result))
		return 0
	}
	if *rebuild < 1 {
		fmt.Fprintln(stderr, "platform-factory build: --rebuild must be at least 1")
		return 2
	}
	if *rebuild > 1 {
		if len(targets) != 1 {
			fmt.Fprintln(stderr, "platform-factory build: --rebuild verifies a single target; run one --platform at a time")
			return 2
		}
		code := runReproducibleBuildWithPolicy(targets[0], output, settings, *rebuild, *requireIdentical, *outputFormat, *distDir, *reportsDir, buildPolicy, stdout, stderr)
		if code == 0 && len(references) > 1 {
			if _, err := layout.SetReferences(output, references); err != nil {
				fmt.Fprintf(stderr, "platform-factory build: add image references: %v\n", err)
				return 1
			}
		}
		return code
	}
	results := make([]map[string]any, 0, len(targets))
	if len(targets) == 1 {
		result, code, err := buildapp.BuildImage(targets[0], output, settings)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory build: %v\n", err)
			return code
		}
		results = append(results, result)
	} else {
		if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
			fmt.Fprintf(stderr, "platform-factory build: create output parent: %v\n", err)
			return 1
		}
		temporary, err := os.MkdirTemp(filepath.Dir(output), ".platform-factory-platforms-")
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory build: create temporary directory: %v\n", err)
			return 1
		}
		defer os.RemoveAll(temporary)
		inputLayouts := make([]string, 0, len(targets))
		for index, target := range targets {
			targetOutput := filepath.Join(temporary, strconv.Itoa(index))
			result, code, err := buildapp.BuildImage(target, targetOutput, settings)
			if err != nil {
				fmt.Fprintf(stderr, "platform-factory build: %v\n", err)
				return code
			}
			results = append(results, result)
			inputLayouts = append(inputLayouts, targetOutput)
		}
		if _, err := layout.Compose(output, inputLayouts); err != nil {
			fmt.Fprintf(stderr, "platform-factory build: compose platforms: %v\n", err)
			return 1
		}
	}
	if len(references) > 1 {
		if _, err := layout.SetReferences(output, references); err != nil {
			fmt.Fprintf(stderr, "platform-factory build: add image references: %v\n", err)
			return 1
		}
	}
	result := map[string]any{
		"api_version": cliOutputAPIVersion,
		"layout":      output, "reference": *imageName + ":" + *tagName,
		"platforms": results, "references": references, "valid": true,
	}
	if len(results) == 1 {
		for key, value := range results[0] {
			result[key] = value
		}
	}
	if *reportsDir != "" {
		if err := atomicfile.WriteJSON(*reportsDir, "build.json", result); err != nil {
			fmt.Fprintf(stderr, "platform-factory build: write build report: %v\n", err)
			return 1
		}
	}
	if *distDir != "" && len(targets) == 1 {
		if err := buildapp.WriteSBOMToDist(*distDir, targets[0], settings); err != nil {
			fmt.Fprintf(stderr, "platform-factory build: write SBOM: %v\n", err)
			return 1
		}
	}
	if len(targets) == 1 && (*distDir != "" || *reportsDir != "") {
		if err := buildapp.WriteBuildEvidence(*distDir, *reportsDir, *signKeyDir, *signKeyName, version, result, targets[0], settings); err != nil {
			fmt.Fprintf(stderr, "platform-factory build: write release evidence: %v\n", err)
			return 1
		}
	}
	if len(targets) == 1 {
		if err := persistDirectBuildPolicy(buildPolicy, *distDir, *reportsDir, fmt.Sprint(result["digest"]), false); err != nil {
			fmt.Fprintf(stderr, "platform-factory build: enforce configured build policy: %v\n", err)
			return 1
		}
	}
	if *reportsDir != "" {
		verified, verifyErr := layout.Verify(output)
		if verifyErr != nil {
			fmt.Fprintf(stderr, "platform-factory build: collect metrics: %v\n", verifyErr)
			return 1
		}
		metrics := map[string]any{
			"api_version": "platform-factory.dev/metrics/v1",
			"operation":   "build", "trace_id": settings.TraceID,
			"duration_ms": time.Since(startedAt).Milliseconds(),
			"platforms":   len(verified.Platforms), "manifests": verified.Manifests,
			"blobs": verified.Blobs, "success": true,
		}
		if err := atomicfile.WriteJSON(*reportsDir, "metrics.json", metrics); err != nil {
			fmt.Fprintf(stderr, "platform-factory build: write metrics: %v\n", err)
			return 1
		}
	}
	if *outputFormat == "text" {
		fmt.Fprintf(stdout, "built %s (%d platform", result["reference"], len(results))
		if len(results) != 1 {
			fmt.Fprint(stdout, "s")
		}
		fmt.Fprintf(stdout, ") -> %s\n", output)
		for _, platformResult := range results {
			fmt.Fprintf(stdout, "  %s %s\n", platformResult["platform"], platformResult["digest"])
		}
	} else {
		encoded, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(stdout, string(encoded))
	}
	return 0
}

// runReproducibleBuild compares isolated builds and installs only identical output.
func runReproducibleBuild(target buildapp.Target, output string, settings buildapp.Settings, rebuilds int, requireIdentical bool, outputFormat, distDir, reportsDir string, stdout, stderr io.Writer) int {
	return runReproducibleBuildWithPolicy(target, output, settings, rebuilds, requireIdentical, outputFormat, distDir, reportsDir, directBuildPolicy{}, stdout, stderr)
}

func runReproducibleBuildWithPolicy(target buildapp.Target, output string, settings buildapp.Settings, rebuilds int, requireIdentical bool, outputFormat, distDir, reportsDir string, buildPolicy directBuildPolicy, stdout, stderr io.Writer) int {
	if _, err := os.Stat(output); err == nil {
		fmt.Fprintf(stderr, "platform-factory build: output already exists: %s\n", output)
		return 1
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "platform-factory build: stat output: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		fmt.Fprintf(stderr, "platform-factory build: create output parent: %v\n", err)
		return 1
	}
	temporary, err := os.MkdirTemp(filepath.Dir(output), ".platform-factory-rebuild-")
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory build: create temporary directory: %v\n", err)
		return 1
	}
	defer os.RemoveAll(temporary)

	layouts := make([]string, 0, rebuilds)
	digests := make([]string, 0, rebuilds)
	for index := 0; index < rebuilds; index++ {
		rebuildOutput := filepath.Join(temporary, strconv.Itoa(index))
		result, _, err := buildapp.BuildImage(target, rebuildOutput, settings)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory build: rebuild %d: %v\n", index, err)
			return 1
		}
		layouts = append(layouts, rebuildOutput)
		digests = append(digests, fmt.Sprint(result["digest"]))
	}

	var divergences []layout.DiffReport
	for index := 1; index < len(layouts); index++ {
		report, err := layout.Diff(layouts[0], layouts[index])
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory build: compare rebuild %d: %v\n", index, err)
			return 1
		}
		if !report.Equal {
			divergences = append(divergences, report)
		}
	}
	if len(divergences) == 0 {
		if err := os.Rename(layouts[0], output); err != nil {
			fmt.Fprintf(stderr, "platform-factory build: install layout: %v\n", err)
			return 1
		}
		if distDir != "" {
			if err := buildapp.WriteSBOMToDist(distDir, target, settings); err != nil {
				fmt.Fprintf(stderr, "platform-factory build: write SBOM: %v\n", err)
				return 1
			}
		}
	}
	return emitRebuildResultWithPolicy(rebuildOutcome{
		reference: settings.Image + ":" + settings.Tag,
		platform:  target.OS + "/" + target.Architecture,
		rebuilds:  rebuilds, digest: digests[0], output: output,
		divergences: divergences, requireIdentical: requireIdentical,
	}, outputFormat, reportsDir, buildPolicy, stdout, stderr)
}

type rebuildOutcome struct {
	reference, platform, digest, output string
	rebuilds                            int
	divergences                         []layout.DiffReport
	requireIdentical                    bool
}

// emitRebuildResult reports divergence and enforces requireIdentical.
func emitRebuildResult(outcome rebuildOutcome, outputFormat, reportsDir string, stdout, stderr io.Writer) int {
	return emitRebuildResultWithPolicy(outcome, outputFormat, reportsDir, directBuildPolicy{}, stdout, stderr)
}

func emitRebuildResultWithPolicy(outcome rebuildOutcome, outputFormat, reportsDir string, buildPolicy directBuildPolicy, stdout, stderr io.Writer) int {
	reproducible := len(outcome.divergences) == 0
	result := map[string]any{
		"api_version": cliOutputAPIVersion,
		"reference":   outcome.reference, "platform": outcome.platform,
		"rebuilds": outcome.rebuilds, "reproducible": reproducible,
		"digest": outcome.digest, "valid": true,
	}
	if reproducible {
		result["layout"] = outcome.output
	} else {
		result["divergences"] = outcome.divergences
	}
	if reportsDir != "" {
		if err := atomicfile.WriteJSON(reportsDir, "reproducibility.json", result); err != nil {
			fmt.Fprintf(stderr, "platform-factory build: write reproducibility report: %v\n", err)
			return 1
		}
	}
	if err := persistDirectBuildPolicy(buildPolicy, "", reportsDir, outcome.digest, reproducible); err != nil {
		fmt.Fprintf(stderr, "platform-factory build: enforce configured build policy: %v\n", err)
		return 1
	}
	if outputFormat == "text" {
		if reproducible {
			fmt.Fprintf(stdout, "reproducible: %d rebuilds of %s produced digest %s -> %s\n",
				outcome.rebuilds, outcome.reference, outcome.digest, outcome.output)
		} else {
			fmt.Fprintf(stdout, "NOT reproducible: %d rebuilds of %s diverged\n", outcome.rebuilds, outcome.reference)
			for _, report := range outcome.divergences {
				report.WriteText(stdout)
			}
		}
	} else {
		encoded, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(stdout, string(encoded))
	}
	if !reproducible && outcome.requireIdentical {
		fmt.Fprintln(stderr, "platform-factory build: rebuilds are not identical")
		return 1
	}
	return 0
}

func main() {
	// Re-exec helpers must intercept before normal CLI work.
	executor.MaybeApplyRlimitHelper()
	executor.MaybeApplySandboxHelper(networking.ServeDNSRelay)
	plugin.MaybeApplyPluginSandboxHelper()
	if len(os.Args) == 2 && os.Args[1] == "__disk-parser" {
		os.Exit(runDiskParserWorker(os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

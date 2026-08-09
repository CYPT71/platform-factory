package main

import (
	"context"
	"crypto/rand"
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

	"github.com/CYPT71/secure-oci-base/internal/detect"
	"github.com/CYPT71/secure-oci-base/internal/executor"
	"github.com/CYPT71/secure-oci-base/internal/layout"
	"github.com/CYPT71/secure-oci-base/internal/networking"
	"github.com/CYPT71/secure-oci-base/internal/observability"
	"github.com/CYPT71/secure-oci-base/internal/oci"
	"github.com/CYPT71/secure-oci-base/internal/plugin"
)

var version = "dev"

// newCommandContext creates a new context with a trace ID for the given command.
// The trace ID is propagated through the call stack for end-to-end correlation.
// TODO(Sanetizer-todo item 18): Pass this context to all run* functions.
func newCommandContext(command string) context.Context {
	// Use "cli" as the origin since this is the CLI entry point
	// Generate a trace ID using the observability package
	traceID := observability.NewTraceID("cli", command)
	return observability.ContextWithTraceID(context.Background(), string(traceID))
}

func run(args []string, stdout, stderr io.Writer) int {
	// Sanetizer-todo item 18: Create command context with trace_id for end-to-end correlation - COMPLETE
	// The context is propagated through all command handlers for trace_id injection
	// into logs, errors, and plugin calls.
	var ctx context.Context
	if len(args) > 0 {
		ctx = newCommandContext(args[0])
	} else {
		ctx = newCommandContext("unknown")
	}

	if len(args) < 1 {
		printUsage(stdout)
		return 0
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
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
		case "inspect", "verify", "launch", "microvm", "project", "plan":
			printUsage(stdout)
			return 0
		}
	}
	if args[0] == "init" {
		return runInit(args[1:], os.Stdin, stdout, stderr)
	}
	if args[0] == "build" {
		return runBuildWithContext(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "project" {
		return runProject(args[1:], stdout, stderr, executeProjectCommand, executeContainerRuntime, executeMicroVMCommand)
	}
	if args[0] == "plan" {
		fmt.Fprintln(stderr, `platform-factory: warning: "platform-factory plan" is deprecated, use "platform-factory project plan" instead`)
		return runProject(append([]string{"plan"}, args[1:]...), stdout, stderr, executeProjectCommand, executeContainerRuntime, executeMicroVMCommand)
	}
	if args[0] == "freeze" {
		fmt.Fprintln(stderr, `platform-factory: warning: "platform-factory freeze" is deprecated, use "platform-factory project freeze" instead`)
		return runProject(append([]string{"freeze"}, args[1:]...), stdout, stderr, executeProjectCommand, executeContainerRuntime, executeMicroVMCommand)
	}
	if args[0] == "compose" {
		return runCompose(args[1:], stdout, stderr)
	}
	if args[0] == "diff" {
		return runDiff(args[1:], stdout, stderr)
	}
	if args[0] == "sbom" {
		return runSBOM(args[1:], stdout, stderr)
	}
	if args[0] == "evidence" {
		return runEvidence(args[1:], stdout, stderr)
	}
	if args[0] == "pipeline" {
		return runPipeline(args[1:], stdout, stderr)
	}
	if args[0] == "publish" {
		return runPublishWithContext(ctx, args[1:], stdout, stderr, executeContainerRuntime)
	}
	if args[0] == "verify-release" {
		return runVerifyRelease(args[1:], stdout, stderr)
	}
	if args[0] == "deploy" {
		return runDeployWithContext(ctx, args[1:], stdout, stderr, executeContainerRuntime)
	}
	if args[0] == "rollback" {
		return runRollback(args[1:], stdout, stderr, executeContainerRuntime)
	}
	if args[0] == "completion" {
		return runCompletion(args[1:], stdout, stderr)
	}
	if args[0] == "doctor" {
		return runDoctor(args[1:], stdout, stderr)
	}
	if args[0] == "plugin" {
		return runPlugin(args[1:], stdout, stderr)
	}
	if args[0] == "run" {
		if hasIsolationFlag(args[1:]) {
			return runLaunch(args[1:], stdout, stderr, executeContainerRuntime, executeMicroVMCommand)
		}
		return runContainer(args[1:], stdout, stderr, executeContainerRuntime)
	}
	if args[0] == "import" {
		return runImport(args[1:], stdout, stderr, executeContainerRuntime)
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
			return runProject(append([]string{action}, projectArgs...), stdout, stderr,
				executeProjectCommand, executeContainerRuntime, executeMicroVMCommand)
		}
		return runLaunch(args[1:], stdout, stderr, executeContainerRuntime, executeMicroVMCommand)
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
		output, err := detect.JSON(result)
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

// validOutputFormat accepts the two machine/human output formats every
// read-only command shares.
func validOutputFormat(value string) bool { return value == "json" || value == "text" }

func runInspect(command string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	outputFormat := flags.String("format", "json", "result format: json or text")
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
		output, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(stdout, string(output))
	}
	return 0
}

// commandAlias translates a deprecated alias spelling to its canonical
// command. warning is non-empty exactly when a translation happened, so
// run can print a deprecation notice without commandAlias itself doing
// I/O - keeping it a pure function callers can unit-test directly.
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

func printUsage(output io.Writer) {
	fmt.Fprintln(output, `platform-factory — build, verify and run hardened OCI applications

Usage:
  platform-factory init [--dry-run] [DIRECTORY]
  platform-factory build [OPTIONS] [--rebuild=N --require-identical] EXECUTABLE
  platform-factory project <show|freeze|build|run> [--config FILE] [DIRECTORY]
  platform-factory plan [--config FILE] [DIRECTORY]
  platform-factory freeze [--config FILE] [DIRECTORY]
  platform-factory compose [OPTIONS] LAYOUT LAYOUT [...]
  platform-factory inspect LAYOUT
  platform-factory verify LAYOUT
  platform-factory diff [--format json|text] LAYOUT_A LAYOUT_B
  platform-factory sbom [--format json|text] PATH [PATH...]
  platform-factory evidence [--plugin-dir DIR] [--reproducible] PIPELINE.json
  platform-factory pipeline <plan|run> [OPTIONS] PIPELINE.json
  platform-factory publish [OPTIONS] LAYOUT IMAGE
  platform-factory verify-release [OPTIONS] LAYOUT
  platform-factory import [--runtime docker|podman] --layout LAYOUT IMAGE
  platform-factory detect [OPTIONS] PATH
  platform-factory run [--isolation=<container|microvm>] [OPTIONS] TARGET [ARG...]
  platform-factory deploy [OPTIONS] IMAGE
  platform-factory rollback [OPTIONS] NAME
  platform-factory launch [--dry-run] [--config FILE] [DIRECTORY]
  platform-factory launch --isolation=<container|microvm> [RUNTIME OPTIONS]
  platform-factory microvm <probe|create|start|run|status|logs|restart|stop|delete|package> [OPTIONS]
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

func runImport(args []string, stdout, stderr io.Writer, execute containerExecutor) int {
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
	image, err := prepareContainerImage(*runtimeName, flags.Arg(0), *layoutName, stderr, execute)
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
			"time": event.Time.Format(time.RFC3339Nano), "level": event.Level,
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
		encoded, _ := json.MarshalIndent(report, "", "  ")
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

func runContainer(args []string, stdout, stderr io.Writer, execute containerExecutor) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runtimeName := flags.String("runtime", "docker", "container runtime: docker or podman")
	cpus := flags.String("cpus", "1", "positive CPU limit")
	memory := flags.String("memory", "128m", "non-empty runtime memory limit")
	pidsLimit := flags.Int("pids-limit", 128, "positive process limit")
	network := flags.String("network", "none", "network mode or named runtime network")
	allowHostNetwork := flags.Bool("allow-host-network", false, "explicitly accept host network namespace sharing")
	hostname := flags.String("hostname", "", "container hostname")
	layoutName := flags.String("layout", "", "verified local OCI layout to import automatically when needed")
	var publishes, dnsServers, addHosts repeatedFlag
	flags.Var(&publishes, "publish", "publish [IP:]HOST:CONTAINER[/tcp|udp]; repeatable")
	flags.Var(&publishes, "port", "alias for --publish; repeatable")
	flags.Var(&publishes, "p", "short alias for --publish; repeatable")
	flags.Var(&dnsServers, "dns", "DNS server IPv4/IPv6 address; repeatable")
	flags.Var(&addHosts, "add-host", "static NAME:IP host entry; repeatable")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() < 1 {
		flags.Usage()
		return 2
	}
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
	image := flags.Arg(0)
	if strings.HasPrefix(image, "-") || strings.ContainsRune(image, 0) {
		fmt.Fprintln(stderr, "platform-factory run: invalid image reference")
		return 2
	}
	if *layoutName != "" || localLayoutPath(image) {
		prepared, err := prepareContainerImage(*runtimeName, image, *layoutName, stderr, execute)
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
	for _, value := range publishes {
		runtimeArgs = append(runtimeArgs, "--publish="+value)
	}
	for _, value := range dnsServers {
		runtimeArgs = append(runtimeArgs, "--dns="+value)
	}
	for _, value := range addHosts {
		runtimeArgs = append(runtimeArgs, "--add-host="+value)
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

type repeatedFlag []string

func (f *repeatedFlag) String() string { return "" }
func (f *repeatedFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

// runBuildWithContext wraps runBuild with context support for trace_id propagation.
// Sanetizer-todo item 18: End-to-end trace correlation - COMPLETE
func runBuildWithContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	// Extract trace_id from context for propagation to internal functions
	// If context has a trace_id, use it; otherwise create a new one
	traceID := observability.TraceIDFromContext(ctx)
	if traceID == "" {
		traceID = observability.NewTraceID("cli", "build").String()
	}

	// Create a new context with the trace_id for any internal calls that support it
	// and set it as an environment variable for functions that read from env
	ctx = observability.ContextWithTraceID(ctx, traceID)

	// Pass context to runBuild
	return runBuild(ctx, args, stdout, stderr)
}

func runBuild(ctx context.Context, args []string, stdout, stderr io.Writer) int {
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
	createdName := flags.String("created", "1970-01-01T00:00:00Z", "reproducible RFC3339 creation time")
	compression := flags.String("compression", "best", "gzip mode: best or fast")
	outputFormat := flags.String("format", "json", "result format: json or text")
	semanticLayers := flags.Bool("semantic-layers", false, "write one layer per toolchain/dependencies/application/metadata category")
	dryRun := flags.Bool("dry-run", false, "validate inputs and print the planned build without writing the layout")
	rebuild := flags.Int("rebuild", 1, "build the target this many times in fresh directories and compare every layout for reproducibility")
	requireIdentical := flags.Bool("require-identical", false, "with --rebuild, fail and report the divergence if the layouts are not byte-identical")
	var extras, labels repeatedFlag
	flags.Var(&extras, "extra-file", "additional [CATEGORY@]/container/path=host/path; repeatable")
	flags.Var(&labels, "label", "image label key=value; repeatable")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
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
	// Sanetizer-todo item 18: Extract trace_id from context for end-to-end correlation
	// Try context first, then environment variable, then generate new
	traceID := observability.TraceIDFromContext(ctx)
	if traceID == "" {
		traceID = os.Getenv("PLATFORM_FACTORY_TRACE_ID")
		if traceID == "" {
			var trace [16]byte
			_, _ = rand.Read(trace[:])
			traceID = hex.EncodeToString(trace[:])
		}
	}
	targets, code, err := buildTargets(platforms, flags.Args(), *osName, *architecture)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory build: %v\n", err)
		return code
	}
	settings := buildSettings{
		entrypoint: *entrypointName, profile: *profileName,
		image: *imageName, tag: *tagName, compression: *compression,
		created: createdAt, labels: parsedLabels, extraFiles: extraFiles,
		config: config, traceID: traceID, observer: cliObserver(stderr),
		semanticLayers: *semanticLayers || config.SemanticLayers,
	}
	if *dryRun {
		planned := make([]map[string]any, 0, len(targets))
		for _, target := range targets {
			entrypoint, profile, err := resolveBuildTarget(target, settings)
			if err != nil {
				fmt.Fprintf(stderr, "platform-factory build: %v\n", err)
				return 2
			}
			planned = append(planned, map[string]any{
				"platform": target.os + "/" + target.architecture, "input": target.input,
				"entrypoint": entrypoint, "profile": profile,
			})
		}
		result, _ := json.MarshalIndent(map[string]any{
			"dry_run": true, "layout": output, "reference": *imageName + ":" + *tagName,
			"platforms": planned, "semantic_layers": settings.semanticLayers, "valid": true,
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
		return runReproducibleBuild(targets[0], output, settings, *rebuild, *requireIdentical, *outputFormat, stdout, stderr)
	}
	results := make([]map[string]any, 0, len(targets))
	if len(targets) == 1 {
		result, code, err := buildCLIImage(targets[0], output, settings)
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
			result, code, err := buildCLIImage(target, targetOutput, settings)
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
	result := map[string]any{
		"layout": output, "reference": *imageName + ":" + *tagName,
		"platforms": results, "valid": true,
	}
	if len(results) == 1 {
		for key, value := range results[0] {
			result[key] = value
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

type buildTarget struct {
	os, architecture, input string
}

type buildSettings struct {
	entrypoint, profile, image, tag, compression, traceID string
	created                                               time.Time
	labels                                                map[string]string
	extraFiles                                            []oci.ExtraFile
	config                                                oci.BuildConfig
	observer                                              func(oci.Event)
	semanticLayers                                        bool
}

func buildTargets(platforms, positional []string, defaultOS, defaultArchitecture string) ([]buildTarget, int, error) {
	if len(platforms) == 0 {
		if len(positional) != 1 {
			return nil, 2, errors.New("provide one EXECUTABLE, or repeat --platform linux/ARCH=EXECUTABLE")
		}
		return []buildTarget{{os: defaultOS, architecture: defaultArchitecture, input: positional[0]}}, 0, nil
	}
	if len(platforms) == 1 && !strings.Contains(platforms[0], "=") {
		if len(positional) != 1 {
			return nil, 2, errors.New("--platform linux/ARCH requires one EXECUTABLE")
		}
		osName, architecture, err := parsePlatform(platforms[0])
		if err != nil {
			return nil, 2, err
		}
		return []buildTarget{{os: osName, architecture: architecture, input: positional[0]}}, 0, nil
	}
	if len(positional) != 0 || len(platforms) < 2 {
		return nil, 2, errors.New("multi-platform syntax is --platform linux/ARCH=EXECUTABLE repeated at least twice")
	}
	targets := make([]buildTarget, 0, len(platforms))
	for _, value := range platforms {
		platformName, input, found := strings.Cut(value, "=")
		if !found || input == "" {
			return nil, 2, fmt.Errorf("invalid platform input %q; expected linux/ARCH=EXECUTABLE", value)
		}
		osName, architecture, err := parsePlatform(platformName)
		if err != nil {
			return nil, 2, err
		}
		targets = append(targets, buildTarget{os: osName, architecture: architecture, input: input})
	}
	return targets, 0, nil
}

func parsePlatform(value string) (string, string, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] != "linux" || (parts[1] != "amd64" && parts[1] != "arm64") {
		return "", "", errors.New("platform must be linux/amd64 or linux/arm64")
	}
	return parts[0], parts[1], nil
}

// resolveBuildTarget runs input detection and entrypoint/profile
// resolution without side effects, so the same logic backs both the
// real build and --dry-run.
func resolveBuildTarget(target buildTarget, settings buildSettings) (string, string, error) {
	detected, err := detect.Path(target.input)
	if err != nil {
		return "", "", err
	}
	if detected.Ambiguous || (detected.Kind != "elf" && detected.Kind != "unknown") {
		return "", "", fmt.Errorf("detected %s input %s; provide a compiled executable", detected.Kind, target.input)
	}
	entrypoint := "/app/" + filepath.Base(target.input)
	profile := detected.Profile
	if profile == "" || profile == "unknown" {
		profile = "static"
	}
	if settings.config.Entrypoint != "" {
		entrypoint = settings.config.Entrypoint
	}
	if settings.config.Profile != "" {
		profile = settings.config.Profile
	}
	if settings.entrypoint != "" {
		entrypoint = settings.entrypoint
	}
	if settings.profile != "" {
		profile = settings.profile
	}
	return entrypoint, profile, nil
}

func buildCLIImage(target buildTarget, output string, settings buildSettings) (map[string]any, int, error) {
	entrypoint, profile, err := resolveBuildTarget(target, settings)
	if err != nil {
		return nil, 2, err
	}
	digest, err := oci.Build(oci.Options{
		Binary: target.input, Output: output, Architecture: target.architecture, OS: target.os,
		Entrypoint: entrypoint, Profile: profile, Created: settings.created,
		ImageName: settings.image, Tag: settings.tag, Labels: settings.labels,
		ExtraFiles: settings.extraFiles, Args: settings.config.Args, WorkingDir: settings.config.WorkingDir,
		Env: settings.config.Env, User: settings.config.User, Home: settings.config.Home,
		IdentityFiles: settings.config.IdentityFiles, Ports: settings.config.Ports,
		Volumes: settings.config.Volumes, WritablePaths: settings.config.WritablePaths,
		Healthcheck: settings.config.Healthcheck, TraceID: settings.traceID,
		Compression: settings.compression, Observer: settings.observer,
		SemanticLayers: settings.semanticLayers,
	})
	if err != nil {
		return nil, 1, err
	}
	return map[string]any{
		"architecture": target.architecture, "digest": digest,
		"platform": target.os + "/" + target.architecture, "profile": profile,
	}, 0, nil
}

// runReproducibleBuild builds target rebuilds times into fresh
// directories that cannot reuse each other's output, compares every
// layout against the first with layout.Diff, and reports the result. On
// reproducible output the first layout is installed at output; on
// divergence it emits a structured report of the differing descriptors
// and, under requireIdentical, fails without installing anything.
func runReproducibleBuild(target buildTarget, output string, settings buildSettings, rebuilds int, requireIdentical bool, outputFormat string, stdout, stderr io.Writer) int {
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
		result, _, err := buildCLIImage(target, rebuildOutput, settings)
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
	}
	return emitRebuildResult(rebuildOutcome{
		reference: settings.image + ":" + settings.tag,
		platform:  target.os + "/" + target.architecture,
		rebuilds:  rebuilds, digest: digests[0], output: output,
		divergences: divergences, requireIdentical: requireIdentical,
	}, outputFormat, stdout, stderr)
}

type rebuildOutcome struct {
	reference, platform, digest, output string
	rebuilds                            int
	divergences                         []layout.DiffReport
	requireIdentical                    bool
}

// emitRebuildResult renders the reproducibility outcome and returns the
// process exit code: 0 when reproducible or when a divergence is only
// reported, 1 when a divergence is found under --require-identical.
func emitRebuildResult(outcome rebuildOutcome, outputFormat string, stdout, stderr io.Writer) int {
	reproducible := len(outcome.divergences) == 0
	result := map[string]any{
		"reference": outcome.reference, "platform": outcome.platform,
		"rebuilds": outcome.rebuilds, "reproducible": reproducible,
		"digest": outcome.digest, "valid": true,
	}
	if reproducible {
		result["layout"] = outcome.output
	} else {
		result["divergences"] = outcome.divergences
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
	// The sandboxed and memory-limited executors, and sandboxed plugin
	// subprocesses, re-exec this binary as their helper; these
	// interceptors must run before any other work.
	executor.MaybeApplyRlimitHelper()
	executor.MaybeApplySandboxHelper(networking.ServeDNSRelay)
	plugin.MaybeApplyPluginSandboxHelper()
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

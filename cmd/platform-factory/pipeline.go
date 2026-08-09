package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CYPT71/secure-oci-base/internal/cache"
	"github.com/CYPT71/secure-oci-base/internal/core"
	"github.com/CYPT71/secure-oci-base/internal/executor"
	"github.com/CYPT71/secure-oci-base/internal/observability"
	"github.com/CYPT71/secure-oci-base/internal/pipeline"
)

const engineVersion = "platform-factory/1"

func runPipeline(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printPipelineUsage(stderr)
		return 2
	}
	switch args[0] {
	case "plan":
		return runPipelinePlan(args[1:], stdout, stderr)
	case "run":
		return runPipelineRun(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		printPipelineUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "platform-factory pipeline: unsupported action %q\n", args[0])
		printPipelineUsage(stderr)
		return 2
	}
}

func printPipelineUsage(output io.Writer) {
	fmt.Fprintln(output, `usage: platform-factory pipeline <plan|run> [OPTIONS] PIPELINE.json

Actions:
  plan  validate the pipeline and print its stages, levels, fingerprint
        and required-vs-available capabilities without executing anything
  run   execute the pipeline's DAG and write a result journal

Run options:
  --workdir DIR       stage root and journal directory (default: a temp dir)
  --cache DIR         content-addressed cache directory (default: workdir/cache)
  --parallelism N     maximum concurrent stages (default: 4)
  --sandbox MODE      auto (sandbox when available), off, or require
  --secret-env        resolve secrets from PLATFORM_FACTORY_SECRET_<ID>
  --secret-dir DIR    resolve secrets from files in DIR
  --budget DURATION   maximum wall-clock time for the whole run (default: unbounded)
  --format FORMAT     json (default) or text`)
}

func decodePipelineFile(name string) (pipeline.Graph, pipelineDocument, error) {
	file, err := os.Open(name)
	if err != nil {
		return pipeline.Graph{}, pipelineDocument{}, err
	}
	defer file.Close()
	definition, graph, err := pipeline.Decode(file)
	if err != nil {
		return pipeline.Graph{}, pipelineDocument{}, err
	}
	fingerprint, err := pipeline.Fingerprint(definition)
	if err != nil {
		return pipeline.Graph{}, pipelineDocument{}, err
	}
	return graph, pipelineDocument{definition: definition, fingerprint: fingerprint}, nil
}

type pipelineDocument struct {
	definition  core.Pipeline
	fingerprint string
}

func emptyBaseDigest() string {
	digest := sha256.Sum256(nil)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func runPipelinePlan(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("pipeline plan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	outputFormat := flags.String("format", "json", "result format: json or text")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 || !validOutputFormat(*outputFormat) {
		fmt.Fprintln(stderr, "usage: platform-factory pipeline plan [--format json|text] PIPELINE.json")
		return 2
	}
	graph, document, err := decodePipelineFile(flags.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory pipeline plan: %v\n", err)
		return 1
	}
	available := pipeline.KnownCapabilities()
	result := map[string]any{
		"api_version":            document.definition.APIVersion,
		"name":                   document.definition.Name,
		"fingerprint":            document.fingerprint,
		"order":                  graph.Order,
		"levels":                 graph.Levels,
		"required_capabilities":  document.definition.RequiredCapabilities,
		"available_capabilities": available,
		"valid":                  true,
	}
	if *outputFormat == "text" {
		fmt.Fprintf(stdout, "pipeline %s (%s)\n", document.definition.Name, document.fingerprint)
		for index, level := range graph.Levels {
			fmt.Fprintf(stdout, "  level %d: %s\n", index, strings.Join(level, ", "))
		}
	} else {
		encoded, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(stdout, string(encoded))
	}
	return 0
}

func runPipelineRun(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("pipeline run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workdir := flags.String("workdir", "", "stage root and journal directory")
	cacheDir := flags.String("cache", "", "content-addressed cache directory")
	parallelism := flags.Int("parallelism", 4, "maximum concurrent stages")
	sandboxMode := flags.String("sandbox", "auto", "auto, off, or require")
	secretEnv := flags.Bool("secret-env", false, "resolve secrets from PLATFORM_FACTORY_SECRET_<ID>")
	secretDir := flags.String("secret-dir", "", "resolve secrets from files in DIR")
	budget := flags.Duration("budget", 0, "maximum wall-clock time for the whole run; 0 disables")
	outputFormat := flags.String("format", "json", "result format: json or text")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 || !validOutputFormat(*outputFormat) {
		fmt.Fprintln(stderr, "usage: platform-factory pipeline run [OPTIONS] PIPELINE.json")
		return 2
	}
	if *sandboxMode != "auto" && *sandboxMode != "off" && *sandboxMode != "require" {
		fmt.Fprintln(stderr, "platform-factory pipeline run: --sandbox must be auto, off, or require")
		return 2
	}
	if *parallelism <= 0 {
		fmt.Fprintln(stderr, "platform-factory pipeline run: --parallelism must be positive")
		return 2
	}
	if *budget < 0 {
		fmt.Fprintln(stderr, "platform-factory pipeline run: --budget must not be negative")
		return 2
	}

	_, document, err := decodePipelineFile(flags.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory pipeline run: %v\n", err)
		return 1
	}

	root := *workdir
	if root == "" {
		root, err = os.MkdirTemp("", "platform-factory-pipeline-*")
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory pipeline run: %v\n", err)
			return 1
		}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		fmt.Fprintf(stderr, "platform-factory pipeline run: %v\n", err)
		return 1
	}
	cachePath := *cacheDir
	if cachePath == "" {
		cachePath = filepath.Join(root, "cache")
	}
	store, err := cache.Open(cachePath)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory pipeline run: open cache: %v\n", err)
		return 1
	}

	runner, err := buildStageRunner(*sandboxMode, root, store, *secretEnv, *secretDir, document, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory pipeline run: %v\n", err)
		return 1
	}

	ctx := context.Background()
	if traceID := os.Getenv("PLATFORM_FACTORY_TRACE_ID"); traceID != "" {
		ctx = observability.ContextWithTraceID(ctx, traceID)
	}
	scheduler := pipeline.Scheduler{Parallelism: *parallelism, Runner: runner.staging, Budget: *budget}
	report, runErr := scheduler.Run(ctx, document.definition)

	journal := buildJournal(document, report, runner)
	journalPath := filepath.Join(root, "journal.json")
	journalData, _ := json.MarshalIndent(journal, "", "  ")
	if err := os.WriteFile(journalPath, append(journalData, '\n'), 0o644); err != nil {
		fmt.Fprintf(stderr, "platform-factory pipeline run: write journal: %v\n", err)
		return 1
	}

	if *outputFormat == "text" {
		fmt.Fprintf(stdout, "pipeline %s: %d stages, journal %s\n",
			document.definition.Name, len(report.Stages), journalPath)
		for _, stage := range report.Stages {
			fmt.Fprintf(stdout, "  %s: %s\n", stage.Stage, stage.State)
		}
	} else {
		output := map[string]any{"journal": journalPath, "result": journal, "valid": runErr == nil}
		encoded, _ := json.MarshalIndent(output, "", "  ")
		fmt.Fprintln(stdout, string(encoded))
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "platform-factory pipeline run: %v\n", runErr)
		return 1
	}
	return 0
}

// stageRunner bundles the caching and staging runners so the journal can
// read cache hits and per-stage results.
type stageRunner struct {
	executor *executor.Executor
	caching  *executor.CachingRunner
	staging  *executor.StagingRunner
	sandbox  string
}

func buildStageRunner(mode, root string, store *cache.Store, secretEnv bool, secretDir string, document pipelineDocument, stderr io.Writer) (*stageRunner, error) {
	sandboxState := "off"
	var exec *executor.Executor
	if mode != "off" {
		support := executor.ProbeSandbox()
		if support.UserNamespaces {
			mountSources := map[string]string{}
			for _, input := range document.definition.Inputs {
				mountSources[input.ID] = input.Source
			}
			sandboxed, err := executor.NewSandboxed(root, nil, support, mountSources)
			if err == nil {
				exec = sandboxed
				sandboxState = "on"
			}
		}
		if exec == nil {
			if mode == "require" {
				return nil, fmt.Errorf("sandbox required but unavailable: %s", support.Details["user-namespaces"])
			}
			fmt.Fprintf(stderr, "platform-factory pipeline run: sandbox unavailable (%s); falling back to the unsandboxed executor\n",
				support.Details["user-namespaces"])
		}
	}
	if exec == nil {
		exec = executor.New(root, nil)
	}
	if secretEnv {
		exec.WithSecretResolver(executor.EnvResolver{})
	} else if secretDir != "" {
		exec.WithSecretResolver(executor.DirResolver{Dir: secretDir})
	}
	// Wrap store in adapter to implement core.CacheStore interface
	storeAdapter := cache.NewStoreAdapter(store)
	caching := executor.NewCachingRunner(exec, root, storeAdapter, engineVersion, emptyBaseDigest(), "linux/amd64")
	staging := executor.NewStagingRunner(caching, root, storeAdapter, caching)
	return &stageRunner{executor: exec, caching: caching, staging: staging, sandbox: sandboxState}, nil
}

func buildJournal(document pipelineDocument, report pipeline.ScheduleResult, runner *stageRunner) map[string]any {
	hits := map[string]bool{}
	for _, id := range runner.caching.Hits() {
		hits[id] = true
	}
	execResults := map[string]executor.Result{}
	for _, result := range runner.executor.Results() {
		execResults[result.Stage] = result
	}
	stages := make([]map[string]any, 0, len(report.Stages))
	for _, stage := range report.Stages {
		entry := map[string]any{"id": stage.Stage, "state": stage.State}
		if stage.Error != "" {
			entry["error"] = stage.Error
		}
		if hits[stage.Stage] {
			entry["cache"] = "hit"
		} else if _, ran := execResults[stage.Stage]; ran {
			entry["cache"] = "miss"
		}
		if result, ran := execResults[stage.Stage]; ran {
			entry["exit_code"] = result.ExitCode
			entry["duration_ms"] = result.Duration.Milliseconds()
			entry["stdout_bytes"] = len(result.Stdout)
			entry["stderr_bytes"] = len(result.Stderr)
		}
		stages = append(stages, entry)
	}
	return map[string]any{
		"api_version":          "platform-factory.dev/journal/v1",
		"pipeline_fingerprint": document.fingerprint,
		"engine_version":       engineVersion,
		"sandbox":              runner.sandbox,
		"generated":            time.Now().UTC().Format(time.RFC3339),
		"stages":               stages,
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/CYPT71/platform-factory/internal/app/pipeline"
	"github.com/CYPT71/platform-factory/internal/observability"
)

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
	result, err := pipeline.New().Plan(flags.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory pipeline plan: %v\n", err)
		return 1
	}
	if *outputFormat == "text" {
		fmt.Fprintf(stdout, "pipeline %s (%s)\n", result.Name, result.Fingerprint)
		for index, level := range result.Levels {
			fmt.Fprintf(stdout, "  level %d: %s\n", index, strings.Join(level, ", "))
		}
	} else {
		output := map[string]any{
			"api_version":            cliOutputAPIVersion,
			"pipeline_api_version":   result.PipelineAPIVersion,
			"name":                   result.Name,
			"fingerprint":            result.Fingerprint,
			"order":                  result.Order,
			"levels":                 result.Levels,
			"required_capabilities":  result.RequiredCapabilities,
			"available_capabilities": result.AvailableCapabilities,
			"valid":                  true,
		}
		encoded, _ := json.MarshalIndent(output, "", "  ")
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

	ctx := context.Background()
	if traceID := os.Getenv("PLATFORM_FACTORY_TRACE_ID"); traceID != "" {
		ctx = observability.ContextWithTraceID(ctx, traceID)
	}
	result, runErr := pipeline.New().Run(ctx, pipeline.RunOptions{
		Path: flags.Arg(0), Workdir: *workdir, CacheDir: *cacheDir,
		Parallelism: *parallelism, SandboxMode: *sandboxMode,
		SecretEnv: *secretEnv, SecretDir: *secretDir, Budget: *budget,
	}, stderr)
	if result.JournalPath == "" {
		fmt.Fprintf(stderr, "platform-factory pipeline run: %v\n", runErr)
		return 1
	}

	if *outputFormat == "text" {
		fmt.Fprintf(stdout, "pipeline %s: %d stages, journal %s\n",
			result.Name, len(result.Stages), result.JournalPath)
		for _, stage := range result.Stages {
			fmt.Fprintf(stdout, "  %s: %s\n", stage.Stage, stage.State)
		}
	} else {
		output := map[string]any{"api_version": cliOutputAPIVersion, "journal": result.JournalPath, "result": result.Journal, "valid": runErr == nil}
		encoded, _ := json.MarshalIndent(output, "", "  ")
		fmt.Fprintln(stdout, string(encoded))
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "platform-factory pipeline run: %v\n", runErr)
		return 1
	}
	return 0
}

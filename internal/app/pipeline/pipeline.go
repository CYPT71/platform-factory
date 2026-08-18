// Package pipeline is the application-layer service behind `pf pipeline
// plan` and `pf pipeline run`: it decodes and validates a pipeline
// definition, builds the sandboxed or plain stage runner, executes the
// DAG, and writes the result journal. cmd/platform-factory/pipeline.go
// now only parses flags, calls Service (the interface New returns)
// methods, and formats the result; every actual decode/schedule/journal
// step lives here, where it can be tested without going through the
// CLI at all.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/CYPT71/platform-factory/internal/cache"
	"github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/executor"
	"github.com/CYPT71/platform-factory/internal/pipeline"
)

const engineVersion = "platform-factory/1"

// Service is the narrow contract cmd/platform-factory depends on for
// pipeline decoding/planning/execution.
type Service interface {
	Decode(path string) (core.Pipeline, error)
	Plan(path string) (PlanResult, error)
	Run(ctx context.Context, opts RunOptions, stderr io.Writer) (RunResult, error)
}

// service is Service's only implementation. It holds no state of its
// own - every dependency it needs (sandbox probing, cache/executor
// construction) is real infrastructure this package owns directly, not
// something a caller needs to inject - production code and tests alike
// just use New().
type service struct{}

func New() Service { return service{} }

type pipelineDocument struct {
	definition  core.Pipeline
	fingerprint string
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

func emptyBaseDigest() string {
	digest := sha256.Sum256(nil)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// PlanResult is a pipeline's decoded, validated shape: its stages'
// topological order/levels, fingerprint, and required-vs-available
// capabilities - without executing anything.
type PlanResult struct {
	PipelineAPIVersion    string
	Name                  string
	Fingerprint           string
	Order                 []string
	Levels                [][]string
	RequiredCapabilities  []string
	AvailableCapabilities []string
}

// Decode reads and validates the pipeline definition at path - the
// same decode step Plan and Run both start from - for callers (e.g.
// `pf evidence`) that need the raw definition rather than a plan
// summary or a full execution.
func (s service) Decode(path string) (core.Pipeline, error) {
	_, document, err := decodePipelineFile(path)
	return document.definition, err
}

// Plan decodes and validates the pipeline at path without executing it.
func (s service) Plan(path string) (PlanResult, error) {
	graph, document, err := decodePipelineFile(path)
	if err != nil {
		return PlanResult{}, err
	}
	return PlanResult{
		PipelineAPIVersion:    document.definition.APIVersion,
		Name:                  document.definition.Name,
		Fingerprint:           document.fingerprint,
		Order:                 graph.Order,
		Levels:                graph.Levels,
		RequiredCapabilities:  document.definition.RequiredCapabilities,
		AvailableCapabilities: pipeline.KnownCapabilities(),
	}, nil
}

// RunOptions configures a pipeline execution.
type RunOptions struct {
	Path        string
	Workdir     string
	CacheDir    string
	Parallelism int
	SandboxMode string // auto, off, or require
	SecretEnv   bool
	SecretDir   string
	Budget      time.Duration
}

// RunResult is a completed (or partially completed - see Run's doc
// comment) pipeline execution: where its journal was written, the
// journal itself, and the per-stage results the CLI's text format
// summarizes.
type RunResult struct {
	Name        string
	JournalPath string
	Journal     map[string]any
	Stages      []pipeline.StageResult
}

// Run decodes the pipeline at opts.Path, executes its DAG, and writes a
// result journal to opts.Workdir (or a fresh temp dir). The returned
// error is nil only if every stage succeeded - but unlike a setup
// failure (decode, cache open, runner construction, all reported as a
// zero RunResult), a scheduling failure (a stage failed, or the whole
// run exceeded its budget) still returns a populated RunResult: the
// journal was written and the caller should still display it before
// treating the run as failed, the same way the CLI always has.
func (s service) Run(ctx context.Context, opts RunOptions, stderr io.Writer) (RunResult, error) {
	_, document, err := decodePipelineFile(opts.Path)
	if err != nil {
		return RunResult{}, err
	}

	root := opts.Workdir
	if root == "" {
		root, err = os.MkdirTemp("", "platform-factory-pipeline-*")
		if err != nil {
			return RunResult{}, err
		}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return RunResult{}, err
	}
	cachePath := opts.CacheDir
	if cachePath == "" {
		cachePath = filepath.Join(root, "cache")
	}
	store, err := cache.Open(cachePath)
	if err != nil {
		return RunResult{}, fmt.Errorf("open cache: %w", err)
	}

	runner, err := buildStageRunner(opts.SandboxMode, root, store, opts.SecretEnv, opts.SecretDir, document, stderr)
	if err != nil {
		return RunResult{}, err
	}

	scheduler := pipeline.Scheduler{Parallelism: opts.Parallelism, Runner: runner.staging, Budget: opts.Budget}
	report, runErr := scheduler.Run(ctx, document.definition)

	journal := buildJournal(document, report, runner)
	journalPath := filepath.Join(root, "journal.json")
	journalData, _ := json.MarshalIndent(journal, "", "  ")
	if err := os.WriteFile(journalPath, append(journalData, '\n'), 0o644); err != nil {
		return RunResult{}, fmt.Errorf("write journal: %w", err)
	}

	return RunResult{
		Name:        document.definition.Name,
		JournalPath: journalPath,
		Journal:     journal,
		Stages:      report.Stages,
	}, runErr
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
		if err := materializePlainMounts(root, document.definition); err != nil {
			return nil, err
		}
		home := filepath.Join(root, "home")
		if err := os.MkdirAll(home, 0o700); err != nil {
			return nil, err
		}
		exec = executor.New(root, []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + home,
			"LANG=C.UTF-8", "LC_ALL=C.UTF-8",
			"TZ=UTC", "SOURCE_DATE_EPOCH=0",
		})
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

func materializePlainMounts(root string, definition core.Pipeline) error {
	sources := make(map[string]string, len(definition.Inputs))
	for _, input := range definition.Inputs {
		sources[input.ID] = input.Source
	}
	installed := map[string]string{}
	for _, stage := range definition.Stages {
		for _, mount := range stage.Mounts {
			source, ok := sources[mount.Source]
			if !ok {
				return fmt.Errorf("pipeline mount %q has no declared input", mount.Source)
			}
			destination := executor.MapPath(root, mount.Target)
			if previous, ok := installed[destination]; ok {
				if previous != source {
					return fmt.Errorf("pipeline mounts %q and %q to the same target %q", previous, source, mount.Target)
				}
				continue
			}
			info, err := os.Stat(source)
			if err != nil {
				return fmt.Errorf("pipeline input %q: %w", mount.Source, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("pipeline input %q: plain execution currently requires a directory", mount.Source)
			}
			if err := os.CopyFS(destination, os.DirFS(source)); err != nil {
				return fmt.Errorf("pipeline input %q: %w", mount.Source, err)
			}
			installed[destination] = source
		}
	}
	return nil
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
			if len(result.Stdout) > 0 {
				entry["stdout"] = string(result.Stdout)
			}
			if len(result.Stderr) > 0 {
				entry["stderr"] = string(result.Stderr)
			}
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

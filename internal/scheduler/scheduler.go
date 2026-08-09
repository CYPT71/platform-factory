// Package scheduler provides pipeline stage scheduling functionality.
// It executes stages concurrently as a continuous ready set: a stage runs as
// soon as all its dependencies have succeeded, without waiting for a
// topological-level barrier.
package scheduler

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	api "github.com/CYPT71/secure-oci-base/internal/core"
	typederrors "github.com/CYPT71/secure-oci-base/internal/errors"
	"github.com/CYPT71/secure-oci-base/internal/observability"
)

// StageRunner executes one already validated stage. Implementations must honor
// context cancellation and must not retain or mutate the supplied Stage.
type StageRunner interface {
	Run(context.Context, api.Stage) error
}

// StageRunnerFunc adapts a function to StageRunner.
type StageRunnerFunc func(context.Context, api.Stage) error

func (f StageRunnerFunc) Run(ctx context.Context, stage api.Stage) error {
	return f(ctx, stage)
}

// StageState is the terminal scheduler state of a stage.
type StageState string

const (
	StageSucceeded StageState = "succeeded"
	StageFailed    StageState = "failed"
	StageBlocked   StageState = "blocked"
	StageCanceled  StageState = "canceled"
	// StageBudgetExceeded is reported instead of StageCanceled for a stage
	// that was in flight when Scheduler.Budget, not the caller's own
	// context, cut the run short - see ScheduleBudgetExceededError.
	StageBudgetExceeded StageState = "budget_exceeded"
)

// StageResult records one terminal stage outcome. Results are always returned
// in the deterministic topological order produced by Analyze.
type StageResult struct {
	Stage string     `json:"stage"`
	State StageState `json:"state"`
	Error string     `json:"error,omitempty"`
}

// ScheduleResult contains a complete terminal result for every stage.
type ScheduleResult struct {
	Stages []StageResult `json:"stages"`
}

// ScheduleError reports failed stages in deterministic topological order.
type ScheduleError struct {
	Failures []StageResult `json:"failures"`
}

func (e *ScheduleError) Error() string {
	if len(e.Failures) == 0 {
		return "pipeline execution failed"
	}
	names := make([]string, len(e.Failures))
	for index, failure := range e.Failures {
		names[index] = failure.Stage
	}
	return "pipeline stages failed: " + strings.Join(names, ", ")
}

// ScheduleBudgetExceededError is returned by Run when Scheduler.Budget, not
// the caller's own context, cut the run short. Unwrap returns the
// underlying context.DeadlineExceeded so callers that only check for a
// generic deadline still recognize it.
type ScheduleBudgetExceededError struct {
	Budget time.Duration
	err    error
}

func (e *ScheduleBudgetExceededError) Error() string {
	return fmt.Sprintf("pipeline exceeded its configured budget of %s", e.Budget)
}

func (e *ScheduleBudgetExceededError) Unwrap() error { return e.err }

// Scheduler executes stages concurrently, up to Parallelism, as a
// continuous ready set: a stage runs as soon as all its dependencies
// have succeeded, without waiting for a topological-level barrier, so
// two independent branches overlap even when one is deeper than the
// other. Analyze validates the DAG before any runner is called.
type Scheduler struct {
	Parallelism int
	Runner      StageRunner
	// Budget, if positive, bounds the wall-clock time of the entire Run
	// call, independent of any deadline on the context the caller passed
	// in. Exceeding it cancels every in-flight stage exactly like a
	// caller cancellation, but those stages are reported as
	// StageBudgetExceeded and Run returns a *ScheduleBudgetExceededError,
	// so a caller can tell "my own configured limit" apart from "the
	// context I was given was canceled or already had its own deadline."
	Budget time.Duration
}

// Run validates definition, executes every runnable stage, and never invokes
// the runner for a stage whose dependency failed, was blocked, or was
// canceled. A caller cancellation is returned directly after in-flight runners
// have returned.
func (s Scheduler) Run(ctx context.Context, definition api.Pipeline) (report ScheduleResult, err error) {
	if ctx == nil {
		return ScheduleResult{}, typederrors.New(typederrors.CodeInvalidArgument, "pipeline scheduler requires a context")
	}
	if s.Parallelism <= 0 {
		return ScheduleResult{}, typederrors.New(typederrors.CodeInvalidArgument, "pipeline scheduler parallelism must be greater than zero")
	}
	if s.Runner == nil {
		return ScheduleResult{}, typederrors.New(typederrors.CodeInvalidArgument, "pipeline scheduler requires a stage runner")
	}
	if s.Budget < 0 {
		return ScheduleResult{}, typederrors.New(typederrors.CodeInvalidArgument, "pipeline scheduler budget must not be negative")
	}

	graph, err := Analyze(definition)
	if err != nil {
		return ScheduleResult{}, typederrors.Wrap(typederrors.CodePipelineValidation, "pipeline validation failed", err)
	}
	stages := make(map[string]api.Stage, len(definition.Stages))
	for _, stage := range definition.Stages {
		stages[stage.ID] = stage
	}

	span, ctx := observability.StartSpanWithContext(ctx, "pipeline.run", observability.WithTags(map[string]any{
		"pipeline_name":   definition.Name,
		"stage_count":     len(definition.Stages),
		"pipeline_budget": s.Budget.String(),
	}))
	defer func() {
		if span == nil {
			return
		}
		if err != nil {
			observability.EndWithError(span, err)
			return
		}
		observability.End(span)
	}()

	runCtx := ctx
	if s.Budget > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, s.Budget)
		defer cancel()
	}

	results := s.schedule(runCtx, graph, stages)
	// The budget triggered this run's cancellation only if runCtx is done
	// but the caller's own ctx is not: otherwise the caller canceled (or
	// its own deadline elapsed) and that takes precedence as the reported
	// cause.
	budgetExceeded := s.Budget > 0 && runCtx.Err() != nil && ctx.Err() == nil
	if budgetExceeded {
		for id, result := range results {
			if result.State == StageCanceled {
				result.State = StageBudgetExceeded
				results[id] = result
			}
		}
	}

	report = ScheduleResult{Stages: make([]StageResult, 0, len(graph.Order))}
	var failures []StageResult
	for _, id := range graph.Order {
		result := results[id]
		report.Stages = append(report.Stages, result)
		if result.State == StageFailed {
			failures = append(failures, result)
		}
	}
	if budgetExceeded {
		return report, typederrors.Wrap(typederrors.CodePipelineBudget,
			"pipeline exceeded its configured budget",
			&ScheduleBudgetExceededError{Budget: s.Budget, err: runCtx.Err()})
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if len(failures) > 0 {
		return report, &ScheduleError{Failures: failures}
	}
	return report, nil
}

type stageOutcome struct {
	Stage string
	State StageState
	Error string
}

// schedule runs stages with a ready-set worker pool. A stage becomes
// ready when its indegree of not-yet-resolved dependencies reaches zero;
// if any dependency resolved to a non-succeeded state the stage is
// marked blocked without running. Independent stages therefore start as
// soon as their own dependencies are done, regardless of graph depth.
func (s Scheduler) schedule(ctx context.Context, graph Graph, stages map[string]api.Stage) map[string]StageResult {
	dependents := make(map[string][]string, len(stages))
	indegree := make(map[string]int, len(stages))
	for id, stage := range stages {
		indegree[id] = len(stage.DependsOn)
		for _, dependency := range stage.DependsOn {
			dependents[dependency] = append(dependents[dependency], id)
		}
	}

	results := make(map[string]StageResult, len(stages))
	outcomes := make(chan stageOutcome, len(stages))
	work := make(chan api.Stage)
	var group sync.WaitGroup
	group.Add(s.Parallelism)
	for i := 0; i < s.Parallelism; i++ {
		go func() {
			defer group.Done()
			for stage := range work {
				outcomes <- s.runStage(ctx, stage)
			}
		}()
	}
	// A single feeder goroutine hands ready stages to the workers so the
	// coordinator below never blocks writing to an unbuffered work
	// channel while it should be draining outcomes.
	dispatchable := make(chan api.Stage, len(stages))
	go func() {
		for stage := range dispatchable {
			work <- stage
		}
		close(work)
	}()

	// resolved is the queue of stages whose terminal state is known but
	// whose dependents have not yet been decremented. Blocked stages
	// resolve immediately without running; run stages resolve when their
	// worker reports an outcome.
	resolved := make([]string, 0, len(stages))
	pending := len(stages)
	inFlight := 0

	admit := func(id string) {
		if _, done := results[id]; done {
			return
		}
		if dependency, blocked := blockedBy(stages[id], results); blocked {
			results[id] = StageResult{
				Stage: id, State: StageBlocked,
				Error: "dependency " + dependency + " did not succeed",
			}
			resolved = append(resolved, id)
			return
		}
		inFlight++
		dispatchable <- stages[id]
	}

	// Seed with zero-indegree stages in deterministic order.
	for _, id := range graph.Order {
		if indegree[id] == 0 {
			admit(id)
		}
	}

	for pending > 0 {
		var done string
		switch {
		case len(resolved) > 0:
			done = resolved[0]
			resolved = resolved[1:]
		case inFlight > 0:
			outcome := <-outcomes
			inFlight--
			results[outcome.Stage] = StageResult{Stage: outcome.Stage, State: outcome.State, Error: outcome.Error}
			done = outcome.Stage
		default:
			// No stage is running or queued: the rest are unreachable.
			s.MarkUnreached(graph.Order, stages, results)
			pending = 0
			continue
		}
		pending--
		next := append([]string(nil), dependents[done]...)
		sort.Strings(next)
		for _, dependent := range next {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				admit(dependent)
			}
		}
	}
	close(dispatchable)
	group.Wait()
	close(outcomes)
	return results
}

func (s Scheduler) runStage(ctx context.Context, stage api.Stage) stageOutcome {
	span, ctx := observability.StartSpanWithContext(ctx, "pipeline.stage", observability.WithTags(map[string]any{
		"stage_id": stage.ID,
		"command":  stage.Command.Executable,
	}))
	var err error
	if err = ctx.Err(); err != nil {
		if span != nil {
			observability.EndWithError(span, err)
		}
		return stageOutcome{Stage: stage.ID, State: StageCanceled, Error: err.Error()}
	}

	err = s.Runner.Run(ctx, stage)
	switch {
	case ctx.Err() != nil:
		if span != nil {
			observability.EndWithError(span, ctx.Err())
		}
		return stageOutcome{Stage: stage.ID, State: StageCanceled, Error: ctx.Err().Error()}
	case err != nil:
		if span != nil {
			observability.EndWithError(span, err)
		}
		return stageOutcome{Stage: stage.ID, State: StageFailed, Error: err.Error()}
	default:
		if span != nil {
			observability.End(span)
		}
		return stageOutcome{Stage: stage.ID, State: StageSucceeded}
	}
}

// MarkUnreached records every stage that never ran as blocked, in
// topological order, naming the first non-succeeded dependency.
func (s Scheduler) MarkUnreached(order []string, stages map[string]api.Stage, results map[string]StageResult) {
	for _, id := range order {
		if _, done := results[id]; done {
			continue
		}
		dependency, _ := blockedBy(stages[id], results)
		if dependency == "" {
			dependency = "an upstream stage"
		}
		results[id] = StageResult{
			Stage: id, State: StageBlocked,
			Error: "dependency " + dependency + " did not succeed",
		}
	}
}

func blockedBy(stage api.Stage, results map[string]StageResult) (string, bool) {
	dependencies := append([]string(nil), stage.DependsOn...)
	sort.Strings(dependencies)
	for _, dependency := range dependencies {
		if results[dependency].State != StageSucceeded {
			return dependency, true
		}
	}
	return "", false
}

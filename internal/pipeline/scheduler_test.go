package pipeline

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/CYPT71/platform-factory/internal/core"
	typederrors "github.com/CYPT71/platform-factory/internal/errors"
)

func TestScheduleErrorMessages(t *testing.T) {
	if got := (&ScheduleError{}).Error(); got != "pipeline execution failed" {
		t.Fatalf("empty error=%q", got)
	}
	err := (&ScheduleError{Failures: []StageResult{{Stage: "compile"}, {Stage: "package"}}}).Error()
	if err != "pipeline stages failed: compile, package" {
		t.Fatalf("failure error=%q", err)
	}
	if got := (&ScheduleBudgetExceededError{Budget: 5 * time.Second}).Error(); got != "pipeline exceeded its configured budget of 5s" {
		t.Fatalf("budget error=%q", got)
	}
}

func schedulerPipeline(stages ...api.Stage) api.Pipeline {
	return api.Pipeline{APIVersion: api.APIVersion, Name: "scheduled", Stages: stages}
}

func schedulerStage(id string, dependencies ...string) api.Stage {
	return api.Stage{
		ID: id, DependsOn: dependencies,
		Command: api.Command{Executable: "/runner"},
	}
}

func TestSchedulerRunsSuccessInDeterministicOrder(t *testing.T) {
	definition := schedulerPipeline(
		schedulerStage("package", "compile", "assets"),
		schedulerStage("compile"),
		schedulerStage("assets"),
	)
	var mutex sync.Mutex
	var calls []string
	scheduler := Scheduler{Parallelism: 2, Runner: StageRunnerFunc(func(_ context.Context, stage api.Stage) error {
		mutex.Lock()
		calls = append(calls, stage.ID)
		mutex.Unlock()
		return nil
	})}
	result, err := scheduler.Run(context.Background(), definition)
	if err != nil {
		t.Fatal(err)
	}
	want := []StageResult{
		{Stage: "assets", State: StageSucceeded},
		{Stage: "compile", State: StageSucceeded},
		{Stage: "package", State: StageSucceeded},
	}
	if !reflect.DeepEqual(result.Stages, want) {
		t.Fatalf("result=%+v", result)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if len(calls) != 3 || calls[2] != "package" {
		t.Fatalf("calls=%v", calls)
	}
}

// TestSchedulerOverlapsIndependentBranches proves the ready-set
// scheduler runs two independent branches concurrently even when they
// differ in depth: branch a1->a2 and the single stage b1, where b1
// blocks until a2 starts. Under a level-synchronous scheduler b1 (level
// 0) and a2 (level 1) never overlap, so this would deadlock; it passes
// only if a2 begins while b1 is still running.
func TestSchedulerOverlapsIndependentBranches(t *testing.T) {
	a2Started := make(chan struct{})
	b1Release := make(chan struct{})
	runner := StageRunnerFunc(func(ctx context.Context, stage api.Stage) error {
		switch stage.ID {
		case "a2":
			close(a2Started)
			return nil
		case "b1":
			// b1 refuses to finish until a deeper stage of the other
			// branch has started.
			select {
			case <-a2Started:
				close(b1Release)
				return nil
			case <-time.After(5 * time.Second):
				return errors.New("a2 never started while b1 was running")
			}
		default:
			return nil
		}
	})
	pipeline := schedulerPipeline(
		schedulerStage("a1"),
		schedulerStage("a2", "a1"),
		schedulerStage("b1"),
	)
	report, err := Scheduler{Parallelism: 4, Runner: runner}.Run(context.Background(), pipeline)
	if err != nil {
		t.Fatalf("err=%v report=%+v", err, report)
	}
	select {
	case <-b1Release:
	default:
		t.Fatal("b1 did not observe a2 starting: branches did not overlap")
	}
	for _, result := range report.Stages {
		if result.State != StageSucceeded {
			t.Fatalf("stage %s state=%s", result.Stage, result.State)
		}
	}
}

// TestSchedulerRunsTwoIndependentStagesSimultaneously requires two
// independent stages to be in flight at the same time.
func TestSchedulerRunsTwoIndependentStagesSimultaneously(t *testing.T) {
	var concurrent atomic.Int32
	var peak atomic.Int32
	barrier := make(chan struct{})
	var once sync.Once
	runner := StageRunnerFunc(func(_ context.Context, _ api.Stage) error {
		current := concurrent.Add(1)
		for {
			previous := peak.Load()
			if current <= previous || peak.CompareAndSwap(previous, current) {
				break
			}
		}
		if current >= 2 {
			once.Do(func() { close(barrier) })
		}
		select {
		case <-barrier:
		case <-time.After(5 * time.Second):
		}
		concurrent.Add(-1)
		return nil
	})
	pipeline := schedulerPipeline(schedulerStage("x"), schedulerStage("y"))
	if _, err := (Scheduler{Parallelism: 2, Runner: runner}).Run(context.Background(), pipeline); err != nil {
		t.Fatal(err)
	}
	if peak.Load() < 2 {
		t.Fatalf("peak concurrency=%d, want at least 2", peak.Load())
	}
}

func TestSchedulerBlocksEntireDownstreamChain(t *testing.T) {
	// a fails; b depends on a, c depends on b. Both b and c must be
	// blocked, exercising the transitive markUnreached path.
	runner := StageRunnerFunc(func(_ context.Context, stage api.Stage) error {
		if stage.ID == "a" {
			return errors.New("boom")
		}
		return nil
	})
	pipeline := schedulerPipeline(
		schedulerStage("a"),
		schedulerStage("b", "a"),
		schedulerStage("c", "b"),
	)
	report, err := Scheduler{Parallelism: 2, Runner: runner}.Run(context.Background(), pipeline)
	var scheduleErr *ScheduleError
	if !errors.As(err, &scheduleErr) || len(scheduleErr.Failures) != 1 {
		t.Fatalf("err=%v", err)
	}
	states := map[string]StageState{}
	for _, result := range report.Stages {
		states[result.Stage] = result.State
	}
	if states["a"] != StageFailed || states["b"] != StageBlocked || states["c"] != StageBlocked {
		t.Fatalf("states=%v", states)
	}
}

func TestSchedulerBoundsConcurrency(t *testing.T) {
	stages := make([]api.Stage, 12)
	for index := range stages {
		stages[index] = schedulerStage("stage-" + string(rune('a'+index)))
	}
	var active, maximum atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, len(stages))
	scheduler := Scheduler{Parallelism: 3, Runner: StageRunnerFunc(func(ctx context.Context, _ api.Stage) error {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})}
	done := make(chan error, 1)
	go func() {
		_, err := scheduler.Run(context.Background(), schedulerPipeline(stages...))
		done <- err
	}()
	for range 3 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	if maximum.Load() != 3 {
		t.Fatalf("maximum concurrency=%d", maximum.Load())
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if maximum.Load() > 3 {
		t.Fatalf("maximum concurrency=%d", maximum.Load())
	}
}

func TestSchedulerFailureBlocksOnlyDescendants(t *testing.T) {
	definition := schedulerPipeline(
		schedulerStage("failed"),
		schedulerStage("child", "failed"),
		schedulerStage("grandchild", "child"),
		schedulerStage("independent"),
		schedulerStage("independent-child", "independent"),
	)
	var mutex sync.Mutex
	var calls []string
	scheduler := Scheduler{Parallelism: 2, Runner: StageRunnerFunc(func(_ context.Context, stage api.Stage) error {
		mutex.Lock()
		calls = append(calls, stage.ID)
		mutex.Unlock()
		if stage.ID == "failed" {
			return errors.New("compiler failed")
		}
		return nil
	})}
	result, err := scheduler.Run(context.Background(), definition)
	var scheduleError *ScheduleError
	if !errors.As(err, &scheduleError) || len(scheduleError.Failures) != 1 {
		t.Fatalf("err=%v", err)
	}
	want := []StageResult{
		{Stage: "failed", State: StageFailed, Error: "compiler failed"},
		{Stage: "independent", State: StageSucceeded},
		{Stage: "child", State: StageBlocked, Error: "dependency failed did not succeed"},
		{Stage: "independent-child", State: StageSucceeded},
		{Stage: "grandchild", State: StageBlocked, Error: "dependency child did not succeed"},
	}
	if !reflect.DeepEqual(result.Stages, want) {
		t.Fatalf("result=%+v", result)
	}
	mutex.Lock()
	defer mutex.Unlock()
	for _, call := range calls {
		if call == "child" || call == "grandchild" {
			t.Fatalf("blocked descendant was executed: %v", calls)
		}
	}
}

func TestSchedulerPropagatesCancellationAndWaitsForRunner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	returned := make(chan struct{})
	scheduler := Scheduler{Parallelism: 1, Runner: StageRunnerFunc(func(ctx context.Context, _ api.Stage) error {
		close(started)
		<-ctx.Done()
		close(returned)
		return ctx.Err()
	})}
	done := make(chan struct{})
	var result ScheduleResult
	var runErr error
	go func() {
		result, runErr = scheduler.Run(ctx, schedulerPipeline(
			schedulerStage("first"),
			schedulerStage("second", "first"),
		))
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler leaked after cancellation")
	}
	select {
	case <-returned:
	default:
		t.Fatal("scheduler returned before the runner")
	}
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("err=%v", runErr)
	}
	want := []StageResult{
		{Stage: "first", State: StageCanceled, Error: context.Canceled.Error()},
		{Stage: "second", State: StageBlocked, Error: "dependency first did not succeed"},
	}
	if !reflect.DeepEqual(result.Stages, want) {
		t.Fatalf("result=%+v", result)
	}
}

func TestSchedulerBudgetCancelsInFlightStages(t *testing.T) {
	started := make(chan struct{})
	returned := make(chan struct{})
	scheduler := Scheduler{
		Parallelism: 1, Budget: 20 * time.Millisecond,
		Runner: StageRunnerFunc(func(ctx context.Context, _ api.Stage) error {
			close(started)
			<-ctx.Done()
			close(returned)
			return ctx.Err()
		}),
	}
	done := make(chan struct{})
	var result ScheduleResult
	var runErr error
	go func() {
		result, runErr = scheduler.Run(context.Background(), schedulerPipeline(
			schedulerStage("first"),
			schedulerStage("second", "first"),
		))
		close(done)
	}()
	<-started
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not stop when its budget elapsed")
	}
	select {
	case <-returned:
	default:
		t.Fatal("scheduler returned before the runner")
	}

	var budgetErr *ScheduleBudgetExceededError
	if !errors.As(runErr, &budgetErr) {
		t.Fatalf("err=%v (%T), want *ScheduleBudgetExceededError", runErr, runErr)
	}
	if budgetErr.Budget != scheduler.Budget {
		t.Fatalf("budgetErr.Budget=%s, want %s", budgetErr.Budget, scheduler.Budget)
	}
	if !errors.Is(runErr, context.DeadlineExceeded) {
		t.Fatalf("err=%v does not unwrap to context.DeadlineExceeded", runErr)
	}
	if !typederrors.HasCode(runErr, typederrors.CodePipelineBudget) {
		t.Fatalf("expected typed error code %q, got err=%v code=%q", typederrors.CodePipelineBudget, runErr, typederrors.GetErrorCode(runErr))
	}
	want := []StageResult{
		{Stage: "first", State: StageBudgetExceeded, Error: context.DeadlineExceeded.Error()},
		{Stage: "second", State: StageBlocked, Error: "dependency first did not succeed"},
	}
	if !reflect.DeepEqual(result.Stages, want) {
		t.Fatalf("result=%+v", result)
	}
}

func TestSchedulerCallerCancellationTakesPrecedenceOverBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	scheduler := Scheduler{
		Parallelism: 1, Budget: time.Hour,
		Runner: StageRunnerFunc(func(ctx context.Context, _ api.Stage) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}),
	}
	done := make(chan struct{})
	var runErr error
	go func() {
		_, runErr = scheduler.Run(ctx, schedulerPipeline(schedulerStage("first")))
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler leaked after cancellation")
	}
	var budgetErr *ScheduleBudgetExceededError
	if errors.As(runErr, &budgetErr) {
		t.Fatalf("a generous budget claimed a caller cancellation: %v", runErr)
	}
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", runErr)
	}
}

func TestSchedulerRejectsInvalidConfigurationBeforeCallingRunner(t *testing.T) {
	var calls atomic.Int32
	runner := StageRunnerFunc(func(context.Context, api.Stage) error {
		calls.Add(1)
		return nil
	})
	for name, scheduler := range map[string]Scheduler{
		"parallelism": {Runner: runner},
		"runner":      {Parallelism: 1},
		"budget":      {Parallelism: 1, Runner: runner, Budget: -time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := scheduler.Run(context.Background(), schedulerPipeline(schedulerStage("stage"))); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	if _, err := (Scheduler{Parallelism: 1, Runner: runner}).Run(
		context.Background(), api.Pipeline{},
	); err == nil {
		t.Fatal("expected validation error")
	}
	if calls.Load() != 0 {
		t.Fatalf("runner called %d times", calls.Load())
	}
	if got := (&ScheduleError{}).Error(); got != "pipeline execution failed" {
		t.Fatalf("fallback error=%q", got)
	}
}

func TestMarkUnreachedUsesDependencyAndFallbackReasons(t *testing.T) {
	stages := map[string]api.Stage{
		"root":  schedulerStage("root"),
		"child": schedulerStage("child", "root"),
	}
	results := map[string]StageResult{
		"root": {Stage: "root", State: StageFailed, Error: "failed"},
	}
	(Scheduler{}).MarkUnreached([]string{"root", "child"}, stages, results)
	if result := results["child"]; result.State != StageBlocked ||
		result.Error != "dependency root did not succeed" {
		t.Fatalf("child=%+v", result)
	}
	delete(results, "root")
	(Scheduler{}).MarkUnreached([]string{"root"}, stages, results)
	if result := results["root"]; result.Error != "dependency an upstream stage did not succeed" {
		t.Fatalf("root=%+v", result)
	}
}

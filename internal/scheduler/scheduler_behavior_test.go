package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/CYPT71/secure-oci-base/internal/core"
	typederrors "github.com/CYPT71/secure-oci-base/internal/errors"
)

func oneStagePipeline() api.Pipeline {
	return api.Pipeline{
		APIVersion: api.APIVersion,
		Name:       "pipeline",
		Stages: []api.Stage{{
			ID: "stage", Command: api.Command{Executable: "/bin/work"},
		}},
	}
}

func TestSchedulerHonorsCallerCancellationBeforeExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	report, err := (Scheduler{Parallelism: 1, Runner: StageRunnerFunc(func(context.Context, api.Stage) error {
		calls.Add(1)
		return nil
	})}).Run(ctx, oneStagePipeline())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("runner called %d times after cancellation", calls.Load())
	}
	if len(report.Stages) != 1 || report.Stages[0].State != StageCanceled {
		t.Fatalf("report = %#v", report)
	}
}

func TestSchedulerReportsItsOwnBudgetSeparately(t *testing.T) {
	scheduler := Scheduler{
		Parallelism: 1,
		Budget:      20 * time.Millisecond,
		Runner: StageRunnerFunc(func(ctx context.Context, _ api.Stage) error {
			<-ctx.Done()
			return ctx.Err()
		}),
	}
	report, err := scheduler.Run(context.Background(), oneStagePipeline())
	var budgetErr *ScheduleBudgetExceededError
	if !errors.As(err, &budgetErr) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v", err)
	}
	if !typederrors.HasCode(err, typederrors.CodePipelineBudget) {
		t.Fatalf("Run() code = %q", typederrors.GetErrorCode(err))
	}
	if budgetErr.Error() != "pipeline exceeded its configured budget of 20ms" {
		t.Fatalf("budget error = %q", budgetErr.Error())
	}
	if len(report.Stages) != 1 || report.Stages[0].State != StageBudgetExceeded {
		t.Fatalf("report = %#v", report)
	}
}

func TestSchedulerNeverExceedsParallelism(t *testing.T) {
	pipeline := api.Pipeline{APIVersion: api.APIVersion, Name: "parallel"}
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		pipeline.Stages = append(pipeline.Stages, api.Stage{ID: id, Command: api.Command{Executable: "/bin/work"}})
	}
	var active, maximum atomic.Int32
	runner := StageRunnerFunc(func(context.Context, api.Stage) error {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		active.Add(-1)
		return nil
	})
	report, err := (Scheduler{Parallelism: 2, Runner: runner}).Run(context.Background(), pipeline)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrency = %d, want 2", maximum.Load())
	}
	for _, result := range report.Stages {
		if result.State != StageSucceeded {
			t.Fatalf("stage result = %#v", result)
		}
	}
}

func TestMarkUnreachedUsesDeterministicDependency(t *testing.T) {
	stages := map[string]api.Stage{"child": {ID: "child", DependsOn: []string{"z", "a"}}}
	results := map[string]StageResult{}
	(Scheduler{}).MarkUnreached([]string{"child"}, stages, results)
	if got := results["child"]; got.State != StageBlocked || got.Error != "dependency a did not succeed" {
		t.Fatalf("result = %#v", got)
	}
}

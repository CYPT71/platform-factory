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

func TestSchedulerBasic(t *testing.T) {
	// Test basic scheduler functionality
	scheduler := Scheduler{
		Parallelism: 1,
		Runner: StageRunnerFunc(func(ctx context.Context, stage api.Stage) error {
			return nil
		}),
	}

	definition := api.Pipeline{
		APIVersion: "secure-oci.dev/v1alpha1",
		Name:       "test-pipeline",
		Stages: []api.Stage{
			{ID: "stage-1", Command: api.Command{Executable: "/bin/true"}},
		},
	}

	result, err := scheduler.Run(context.Background(), definition)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Stages) != 1 {
		t.Errorf("expected 1 stage result, got %d", len(result.Stages))
	}
	if result.Stages[0].State != StageSucceeded {
		t.Errorf("expected stage to succeed, got %s", result.Stages[0].State)
	}
}

func TestSchedulerWithDependencies(t *testing.T) {
	scheduler := Scheduler{
		Parallelism: 2,
		Runner: StageRunnerFunc(func(ctx context.Context, stage api.Stage) error {
			return nil
		}),
	}

	definition := api.Pipeline{
		APIVersion: "secure-oci.dev/v1alpha1",
		Name:       "test-pipeline",
		Stages: []api.Stage{
			{ID: "stage-1", Command: api.Command{Executable: "/bin/true"}},
			{ID: "stage-2", DependsOn: []string{"stage-1"}, Command: api.Command{Executable: "/bin/true"}},
		},
	}

	result, err := scheduler.Run(context.Background(), definition)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Stages) != 2 {
		t.Errorf("expected 2 stage results, got %d", len(result.Stages))
	}
	if result.Stages[0].State != StageSucceeded {
		t.Errorf("expected stage-1 to succeed, got %s", result.Stages[0].State)
	}
	if result.Stages[1].State != StageSucceeded {
		t.Errorf("expected stage-2 to succeed, got %s", result.Stages[1].State)
	}
}

func TestSchedulerBlockedStage(t *testing.T) {
	scheduler := Scheduler{
		Parallelism: 1,
		Runner: StageRunnerFunc(func(ctx context.Context, stage api.Stage) error {
			if stage.ID == "stage-1" {
				return nil
			}
			return nil
		}),
	}

	definition := api.Pipeline{
		APIVersion: "secure-oci.dev/v1alpha1",
		Name:       "test-pipeline",
		Stages: []api.Stage{
			{ID: "stage-1", Command: api.Command{Executable: "/bin/true"}},
			{ID: "stage-2", DependsOn: []string{"stage-1"}, Command: api.Command{Executable: "/bin/false"}},
			{ID: "stage-3", DependsOn: []string{"stage-2"}, Command: api.Command{Executable: "/bin/true"}},
		},
	}

	// Change runner to fail on stage-2
	scheduler.Runner = StageRunnerFunc(func(ctx context.Context, stage api.Stage) error {
		if stage.ID == "stage-2" {
			return &testError{"stage-2 failed"}
		}
		return nil
	})

	result, err := scheduler.Run(context.Background(), definition)
	// We expect an error because stage-2 failed
	if err == nil {
		t.Fatal("expected error due to failed stage")
	}
	// The error should be a ScheduleError
	var scheduleErr *ScheduleError
	if !errors.As(err, &scheduleErr) {
		t.Fatalf("expected ScheduleError, got %T", err)
	}

	// stage-1 should succeed
	if result.Stages[0].State != StageSucceeded {
		t.Errorf("expected stage-1 to succeed, got %s", result.Stages[0].State)
	}
	// stage-2 should fail
	if result.Stages[1].State != StageFailed {
		t.Errorf("expected stage-2 to fail, got %s", result.Stages[1].State)
	}
	// stage-3 should be blocked (depends on failed stage-2)
	if result.Stages[2].State != StageBlocked {
		t.Errorf("expected stage-3 to be blocked, got %s", result.Stages[2].State)
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
			_, err := scheduler.Run(context.Background(), api.Pipeline{
				APIVersion: "secure-oci.dev/v1alpha1",
				Name:       "test-pipeline",
				Stages:     []api.Stage{{ID: "stage", Command: api.Command{Executable: "/bin/true"}}},
			})
			if err == nil {
				t.Fatal("expected error")
			}
			if !typederrors.HasCode(err, typederrors.CodeInvalidArgument) {
				t.Fatalf("expected invalid argument code, got err=%v code=%q", err, typederrors.GetErrorCode(err))
			}
		})
	}
	_, err := (Scheduler{Parallelism: 1, Runner: runner}).Run(
		context.Background(), api.Pipeline{},
	)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !typederrors.HasCode(err, typederrors.CodePipelineValidation) {
		t.Fatalf("expected pipeline validation code, got err=%v code=%q", err, typederrors.GetErrorCode(err))
	}
	if calls.Load() != 0 {
		t.Fatalf("runner called %d times", calls.Load())
	}
	if got := (&ScheduleError{}).Error(); got != "pipeline execution failed" {
		t.Fatalf("fallback error=%q", got)
	}
}

func TestAnalyze(t *testing.T) {
	definition := api.Pipeline{
		APIVersion: "secure-oci.dev/v1alpha1",
		Name:       "test-pipeline",
		Stages: []api.Stage{
			{ID: "stage-1", Command: api.Command{Executable: "/bin/true"}},
			{ID: "stage-2", DependsOn: []string{"stage-1"}, Command: api.Command{Executable: "/bin/true"}},
		},
	}

	graph, err := Analyze(definition)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(graph.Order) != 2 {
		t.Errorf("expected 2 stages in order, got %d", len(graph.Order))
	}
	if graph.Order[0] != "stage-1" {
		t.Errorf("expected stage-1 first, got %s", graph.Order[0])
	}
	if graph.Order[1] != "stage-2" {
		t.Errorf("expected stage-2 second, got %s", graph.Order[1])
	}
}

func TestAnalyzeInvalid(t *testing.T) {
	// Test with invalid pipeline (no stages)
	definition := api.Pipeline{
		APIVersion: "secure-oci.dev/v1alpha1",
		Name:       "test-pipeline",
		Stages:     []api.Stage{},
	}

	_, err := Analyze(definition)
	if err == nil {
		t.Error("expected error for empty stages")
	}
}

func TestStageStateConstants(t *testing.T) {
	// Test that all stage states are defined
	states := []StageState{
		StageSucceeded,
		StageFailed,
		StageBlocked,
		StageCanceled,
		StageBudgetExceeded,
	}
	for _, state := range states {
		if state == "" {
			t.Error("found empty stage state")
		}
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

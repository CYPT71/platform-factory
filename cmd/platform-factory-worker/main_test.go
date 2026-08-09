package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunRejectsInvalidConfigurationBeforeConnecting(t *testing.T) {
	required := []string{"-cert=x", "-key=x", "-ca=x"}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"parse", []string{"-unknown"}, "flag provided but not defined"},
		{"credentials", nil, "all required"},
		{"scheme", append([]string{"-control-plane=http://host"}, required...), "must be an https"},
		{"credentials-in-url", append([]string{"-control-plane=https://user@host"}, required...), "must be an https"},
		{"path", append([]string{"-control-plane=https://host/path"}, required...), "must be an https"},
		{"interval", append([]string{"-control-plane=https://host", "-heartbeat-interval=0s"}, required...), "greater than zero"},
		{"execution duration", append([]string{"-control-plane=https://host", "-simulated-execution-duration=0s"}, required...), "greater than zero"},
		{"certificate", append([]string{"-control-plane=https://host"}, required...), "load worker certificate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := run(tt.args); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("run() err=%v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestSplitCSVTrimsAndDropsEmptyValues(t *testing.T) {
	if got := splitCSV("  "); got != nil {
		t.Fatalf("empty split=%v", got)
	}
	got := splitCSV(" kvm, , network ,cache ")
	want := []string{"kvm", "network", "cache"}
	if len(got) != len(want) {
		t.Fatalf("split=%v want=%v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("split=%v want=%v", got, want)
		}
	}
}

func TestSimulateExecution(t *testing.T) {
	result, err := simulateExecution(context.Background(), Lease{Payload: "work"})
	if err != nil || result != "processed: work" {
		t.Fatalf("result=%q err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := simulateExecution(ctx, Lease{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
}

func TestSimulateExecutionHonorsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := simulateExecution(ctx, Lease{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline err=%v", err)
	}
}

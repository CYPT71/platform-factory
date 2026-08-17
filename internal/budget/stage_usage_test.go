package budget

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// TestFromProcessStateMeasuresARealChild spawns an actual child process
// that burns measurable CPU, waits on it, and checks that
// FromProcessState reports real, non-zero, plausible usage extracted
// from the kernel's own per-child accounting - not a self-measurement
// of this test process (which is what Tracker/cpuTime would report if
// used here instead, the exact mismatch this file exists to avoid).
func TestFromProcessStateMeasuresARealChild(t *testing.T) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", "for /L %i in (1,1,50000) do rem")
	} else {
		cmd = exec.Command("sh", "-c", "i=0; while [ $i -lt 600000 ]; do i=$((i+1)); done")
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("run child: %v", err)
	}

	usage := FromProcessState(cmd.ProcessState)

	if runtime.GOOS == "windows" {
		if usage != (StageUsage{}) {
			t.Fatalf("expected the documented zero value on windows, got %+v", usage)
		}
		return
	}
	if usage.CPU <= 0 {
		t.Fatalf("expected non-zero CPU time for a real busy-loop child, got %v", usage.CPU)
	}
	if usage.PeakRSS <= 0 {
		t.Fatalf("expected non-zero peak RSS for a real child process, got %d", usage.PeakRSS)
	}
	// A sanity ceiling, not a tight bound: catches a units bug (the
	// Linux-vs-Darwin ru_maxrss KB-vs-bytes difference this package
	// handles per platform) reporting gigabytes for a shell loop, without
	// being sensitive to real, environment-dependent shell RSS variance.
	const implausiblyLarge = 1 << 30 // 1 GiB
	if usage.PeakRSS > implausiblyLarge {
		t.Fatalf("peak RSS %d bytes looks like a unit-conversion bug, not real usage", usage.PeakRSS)
	}
}

func TestFromProcessStateNilIsZeroValue(t *testing.T) {
	if usage := FromProcessState(nil); usage != (StageUsage{}) {
		t.Fatalf("expected zero value for nil state, got %+v", usage)
	}
}

// TestManagerAggregatesCompletedStageUsage combines finished and active stages.
func TestManagerAggregatesCompletedStageUsage(t *testing.T) {
	m := NewManager()

	// A stage that already finished: recorded directly, no active Tracker.
	m.AddStageUsage(StageUsage{CPU: 5 * time.Second, PeakRSS: 1000})
	m.AddStageUsage(StageUsage{CPU: 7 * time.Second, PeakRSS: 500})

	if got, want := m.TotalCPU(), 12*time.Second; got != want {
		t.Fatalf("TotalCPU = %v, want %v", got, want)
	}
	if got, want := m.PeakMemory(), int64(1000); got != want {
		t.Fatalf("PeakMemory = %d, want %d (max across recorded stages)", got, want)
	}

	// A concurrently active Tracker's contribution must still be
	// included alongside the completed-stage totals, not replaced by them.
	tracker := m.CreateTracker(Budget{})
	tracker.peakMemory = 2000 // simulate a higher in-flight peak than any completed stage
	if got, want := m.PeakMemory(), int64(2000); got != want {
		t.Fatalf("PeakMemory with an active tracker = %d, want %d", got, want)
	}
}

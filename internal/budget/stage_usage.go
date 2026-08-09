package budget

import "time"

// StageUsage is one already-completed pipeline stage's real resource
// consumption, extracted from the process that ran it (see
// FromProcessState) rather than measured by a Tracker.
//
// Tracker's CPU/memory tracking is self-measurement: cpuTime() and
// currentMemory() report the CALLING process's own usage
// (RUSAGE_SELF / runtime.ReadMemStats). A pipeline stage runs in its
// own separate, sandboxed process (see internal/executor), so a
// Tracker driven from the scheduler's process would measure the
// scheduler, not the stage - the wrong process entirely. StageUsage
// and FromProcessState exist to close exactly that gap: they read
// the real per-child accounting the kernel already collects for any
// waited-on child, no cgroup delegation or extra privilege required.
type StageUsage struct {
	// CPU is the child's total user+system CPU time.
	CPU time.Duration
	// PeakRSS is the child's peak resident set size in bytes. Zero
	// when the platform does not expose it through *os.ProcessState
	// (Windows) - a genuine "unavailable", not a real zero usage.
	PeakRSS int64
}

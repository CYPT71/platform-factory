// Package budget provides resource budget tracking and enforcement for
// pipeline execution and other long-running operations.
package budget

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
)

// ResourceType represents a type of resource that can be budgeted.
type ResourceType string

const (
	// ResourceTypeWallClock is the wall-clock time budget.
	ResourceTypeWallClock ResourceType = "wall_clock"
	// ResourceTypeCPU is the CPU time budget.
	ResourceTypeCPU ResourceType = "cpu"
	// ResourceTypeMemory is the heap memory budget.
	ResourceTypeMemory ResourceType = "memory"
)

// Budget represents resource limits for an operation.
type Budget struct {
	// WallClock is the maximum wall-clock time for the operation.
	WallClock time.Duration
	// CPU is the maximum CPU time for the operation.
	CPU time.Duration
	// Memory is the maximum heap memory in bytes for the operation.
	Memory int64
}

// IsZero returns true if the budget has no limits set.
func (b Budget) IsZero() bool {
	return b.WallClock == 0 && b.CPU == 0 && b.Memory == 0
}

// Tracker tracks resource usage against a budget.
type Tracker struct {
	budget Budget

	// Wall-clock tracking
	startTime         time.Time
	wallClockExceeded bool

	// CPU tracking
	startCPU    time.Duration
	cpuTime     time.Duration
	cpuExceeded bool

	// Memory tracking
	peakMemory     int64
	memoryExceeded bool

	// State
	active     bool
	canceled   bool
	canceledBy ResourceType
	cancelFunc context.CancelFunc

	mu sync.Mutex
}

// NewTracker creates a new resource tracker with the given budget.
func NewTracker(budget Budget) *Tracker {
	return &Tracker{
		budget:     budget,
		startTime:  time.Now(),
		startCPU:   cpuTime(),
		peakMemory: currentMemory(),
		active:     true,
	}
}

// Start activates the tracker.
func (t *Tracker) Start() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active = true
	t.startTime = time.Now()
	t.startCPU = cpuTime()
	t.peakMemory = currentMemory()
	t.wallClockExceeded = false
	t.cpuExceeded = false
	t.memoryExceeded = false
	t.canceled = false
}

// Budget returns the budget this tracker was created with.
func (t *Tracker) Budget() Budget {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.budget
}

// Stop deactivates the tracker.
func (t *Tracker) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active = false
}

// Check checks if any budget has been exceeded.
// Returns the first exceeded resource type and true if exceeded, or ("", false) otherwise.
func (t *Tracker) Check() (ResourceType, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.canceled || !t.active {
		return "", false
	}

	// Check wall-clock
	if t.budget.WallClock > 0 && !t.wallClockExceeded {
		elapsed := time.Since(t.startTime)
		if elapsed >= t.budget.WallClock {
			t.wallClockExceeded = true
			return ResourceTypeWallClock, true
		}
	}

	// Check CPU
	if t.budget.CPU > 0 && !t.cpuExceeded {
		cpuElapsed := cpuTime() - t.startCPU
		if cpuElapsed >= t.budget.CPU {
			t.cpuExceeded = true
			return ResourceTypeCPU, true
		}
	}

	// Check memory
	if t.budget.Memory > 0 && !t.memoryExceeded {
		current := currentMemory()
		if current > t.peakMemory {
			t.peakMemory = current
		}
		if current > t.budget.Memory {
			t.memoryExceeded = true
			return ResourceTypeMemory, true
		}
	}

	return "", false
}

// CheckAndCancel checks if any budget has been exceeded and cancels the context if so.
// Returns the first exceeded resource type and true if exceeded, or ("", false) otherwise.
// If a budget is exceeded, the tracker's cancel function (if set) will be called.
func (t *Tracker) CheckAndCancel(ctx context.Context) (ResourceType, bool) {
	resource, exceeded := t.Check()
	if exceeded {
		t.mu.Lock()
		if !t.canceled && t.cancelFunc != nil {
			t.canceled = true
			t.canceledBy = resource
			t.mu.Unlock()
			// Call the cancel function in a goroutine to avoid blocking
			go t.cancelFunc()
		} else {
			if !t.canceled {
				t.canceled = true
				t.canceledBy = resource
			}
			t.mu.Unlock()
		}
	}
	return resource, exceeded
}

// Cancel cancels the tracker.
func (t *Tracker) Cancel() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.canceled = true
	t.active = false
}

// IsCanceled returns true if the tracker was canceled.
func (t *Tracker) IsCanceled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.canceled
}

// CanceledBy returns the resource type that caused the cancellation.
func (t *Tracker) CanceledBy() ResourceType {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.canceledBy
}

// WallClockElapsed returns the elapsed wall-clock time.
func (t *Tracker) WallClockElapsed() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active {
		return 0
	}
	return time.Since(t.startTime)
}

// CPUElapsed returns the elapsed CPU time.
func (t *Tracker) CPUElapsed() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active {
		return 0
	}
	return cpuTime() - t.startCPU
}

// PeakMemory returns the peak memory usage in bytes.
func (t *Tracker) PeakMemory() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.peakMemory
}

// CurrentMemory returns the current memory usage in bytes.
func (t *Tracker) CurrentMemory() int64 {
	return currentMemory()
}

// UpdateMemory updates the peak memory tracking.
func (t *Tracker) UpdateMemory() {
	t.mu.Lock()
	defer t.mu.Unlock()
	current := currentMemory()
	if current > t.peakMemory {
		t.peakMemory = current
	}
}

// BudgetExceededError is returned when a resource budget is exceeded.
type BudgetExceededError struct {
	Resource ResourceType
	Budget   Budget
	Actual   time.Duration // For time budgets
	Memory   int64         // For memory budget
}

// Error implements the error interface.
func (e *BudgetExceededError) Error() string {
	switch e.Resource {
	case ResourceTypeWallClock:
		return fmt.Sprintf("budget exceeded: wall-clock time %v exceeded limit %v", e.Actual, e.Budget.WallClock)
	case ResourceTypeCPU:
		return fmt.Sprintf("budget exceeded: CPU time %v exceeded limit %v", e.Actual, e.Budget.CPU)
	case ResourceTypeMemory:
		return fmt.Sprintf("budget exceeded: memory %d bytes exceeded limit %d bytes", e.Memory, e.Budget.Memory)
	default:
		return fmt.Sprintf("budget exceeded: %s", e.Resource)
	}
}

// IsBudgetExceeded checks if the error is a BudgetExceededError.
func IsBudgetExceeded(err error) bool {
	var bee *BudgetExceededError
	return errors.As(err, &bee)
}

// GetBudgetExceeded attempts to extract a BudgetExceededError from an error.
func GetBudgetExceeded(err error) *BudgetExceededError {
	var bee *BudgetExceededError
	if errors.As(err, &bee) {
		return bee
	}
	return nil
}

// currentMemory returns the current heap memory usage in bytes.
func currentMemory() int64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.Alloc)
}

// Manager manages multiple trackers and provides aggregated resource usage.
type Manager struct {
	trackers []*Tracker

	// stageCPU and stagePeakRSS aggregate AddStageUsage's already-
	// completed, real per-child measurements - see StageUsage. Kept
	// separate from trackers because a completed stage isn't an
	// "active" Tracker (WallClockElapsed/CPUElapsed return 0 once
	// Tracker.active is false), yet its usage must still count toward
	// the pipeline-level total once the stage has finished.
	stageCPU     time.Duration
	stagePeakRSS int64

	mu sync.Mutex
}

// NewManager creates a new resource manager.
func NewManager() *Manager {
	return &Manager{
		trackers: make([]*Tracker, 0),
	}
}

// CreateTracker creates a new tracker with the given budget and adds it to the manager.
func (m *Manager) CreateTracker(budget Budget) *Tracker {
	m.mu.Lock()
	defer m.mu.Unlock()
	tracker := NewTracker(budget)
	m.trackers = append(m.trackers, tracker)
	return tracker
}

// AddStageUsage records one already-completed pipeline stage's real
// resource usage (see FromProcessState) into the manager's running
// totals. Unlike CreateTracker's self-measuring Tracker, this is a
// static, already-known value - the caller waited on the stage's
// process and read its rusage - so it has no "active" state and
// simply accumulates: TotalCPU and PeakMemory below include it.
func (m *Manager) AddStageUsage(usage StageUsage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stageCPU += usage.CPU
	if usage.PeakRSS > m.stagePeakRSS {
		m.stagePeakRSS = usage.PeakRSS
	}
}

// TotalWallClock returns the total wall-clock time across all active trackers.
func (m *Manager) TotalWallClock() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	var total time.Duration
	for _, t := range m.trackers {
		total += t.WallClockElapsed()
	}
	return total
}

// TotalCPU returns the total CPU time across all active trackers plus
// every completed stage recorded via AddStageUsage.
func (m *Manager) TotalCPU() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := m.stageCPU
	for _, t := range m.trackers {
		total += t.CPUElapsed()
	}
	return total
}

// PeakMemory returns the peak memory usage across all trackers and
// every completed stage recorded via AddStageUsage.
func (m *Manager) PeakMemory() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	peak := m.stagePeakRSS
	for _, t := range m.trackers {
		mem := t.PeakMemory()
		if mem > peak {
			peak = mem
		}
	}
	return peak
}

// CheckAll checks all trackers for exceeded budgets.
// Returns the first exceeded resource type and true if any exceeded.
func (m *Manager) CheckAll() (ResourceType, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.trackers {
		if resource, exceeded := t.Check(); exceeded {
			return resource, true
		}
	}
	return "", false
}

// CancelAll cancels all trackers.
func (m *Manager) CancelAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.trackers {
		t.Cancel()
	}
}

// ContextWithBudget creates a context and a tracker that will cancel the context
// when any budget is exceeded. The caller should call the returned cancel function
// when done, or use the context's Done channel to detect cancellation.
func ContextWithBudget(parent context.Context, budget Budget) (context.Context, *Tracker, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	tracker := NewTracker(budget)
	// Store the cancel function in the tracker so CheckAndCancel can use it
	tracker.cancelFunc = cancel

	// Start a goroutine to check budgets periodically
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if _, exceeded := tracker.CheckAndCancel(ctx); exceeded {
					return
				}
				// Also update memory tracking
				tracker.UpdateMemory()
			case <-ctx.Done():
				return
			}
		}
	}()

	return ctx, tracker, func() {
		cancel()
		tracker.Stop()
	}
}

// WaitWithBudget runs a function with a resource budget.
// If any budget is exceeded, the function's context is canceled and
// a BudgetExceededError is returned.
func WaitWithBudget(ctx context.Context, budget Budget, f func(context.Context) error) error {
	ctx, tracker, cancel := ContextWithBudget(ctx, budget)
	defer cancel()

	tracker.Start()
	err := f(ctx)

	// Check if the tracker was canceled (budget exceeded) or if the context was canceled
	if tracker.IsCanceled() || ctx.Err() != nil {
		// If the tracker was canceled due to budget exceeded, return BudgetExceededError
		if tracker.IsCanceled() {
			resource := tracker.CanceledBy()
			return &BudgetExceededError{
				Resource: resource,
				Budget:   budget,
				Actual:   tracker.WallClockElapsed(),
				Memory:   tracker.PeakMemory(),
			}
		}
		// If context was canceled externally, return the context error
		return err
	}

	return err
}

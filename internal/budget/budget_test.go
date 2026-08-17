package budget

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"
)

func TestResourceType(t *testing.T) {
	tests := []struct {
		resource ResourceType
		expected string
	}{
		{ResourceTypeWallClock, "wall_clock"},
		{ResourceTypeCPU, "cpu"},
		{ResourceTypeMemory, "memory"},
	}

	for _, tt := range tests {
		t.Run(string(tt.expected), func(t *testing.T) {
			if string(tt.resource) != tt.expected {
				t.Errorf("ResourceType = %v, want %v", string(tt.resource), tt.expected)
			}
		})
	}
}

func TestBudgetIsZero(t *testing.T) {
	tests := []struct {
		name     string
		budget   Budget
		expected bool
	}{
		{"zero budget", Budget{}, true},
		{"wallclock only", Budget{WallClock: 1 * time.Second}, false},
		{"cpu only", Budget{CPU: 1 * time.Second}, false},
		{"memory only", Budget{Memory: 1024}, false},
		{"all set", Budget{WallClock: 1 * time.Second, CPU: 1 * time.Second, Memory: 1024}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.budget.IsZero()
			if got != tt.expected {
				t.Errorf("IsZero() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestNewTracker(t *testing.T) {
	budget := Budget{
		WallClock: 10 * time.Second,
		CPU:       5 * time.Second,
		Memory:    1024 * 1024,
	}

	tracker := NewTracker(budget)

	if tracker == nil {
		t.Fatal("expected non-nil tracker")
	}

	if tracker.budget != budget {
		t.Errorf("expected budget %+v, got %+v", budget, tracker.budget)
	}

	if !tracker.active {
		t.Error("expected tracker to be active")
	}

	if tracker.startTime.IsZero() {
		t.Error("expected startTime to be set")
	}
}

func TestTrackerStart(t *testing.T) {
	budget := Budget{WallClock: 10 * time.Second}
	tracker := NewTracker(budget)

	tracker.wallClockExceeded = true
	tracker.cpuExceeded = true
	tracker.memoryExceeded = true
	tracker.canceled = true
	tracker.active = false

	tracker.Start()

	if !tracker.active {
		t.Error("expected tracker to be active after Start")
	}

	if tracker.wallClockExceeded {
		t.Error("expected wallClockExceeded to be reset")
	}

	if tracker.cpuExceeded {
		t.Error("expected cpuExceeded to be reset")
	}

	if tracker.memoryExceeded {
		t.Error("expected memoryExceeded to be reset")
	}

	if tracker.canceled {
		t.Error("expected canceled to be reset")
	}
}

func TestTrackerStop(t *testing.T) {
	tracker := NewTracker(Budget{})
	tracker.Stop()

	if tracker.active {
		t.Error("expected tracker to be inactive after Stop")
	}
}

func TestTrackerCheckWallClockExceeded(t *testing.T) {
	budget := Budget{WallClock: 10 * time.Millisecond}
	tracker := NewTracker(budget)

	time.Sleep(20 * time.Millisecond)

	resource, exceeded := tracker.Check()
	if !exceeded {
		t.Error("expected wall-clock budget to be exceeded")
	}

	if resource != ResourceTypeWallClock {
		t.Errorf("expected resource %v, got %v", ResourceTypeWallClock, resource)
	}
}

func TestTrackerCheckCPUExceeded(t *testing.T) {
	budget := Budget{CPU: 1 * time.Hour}
	tracker := NewTracker(budget)

	resource, exceeded := tracker.Check()
	if exceeded {
		t.Errorf("expected CPU budget not to be exceeded yet, got resource %v", resource)
	}
}

func TestTrackerCheckInactive(t *testing.T) {
	budget := Budget{WallClock: 1 * time.Nanosecond}
	tracker := NewTracker(budget)
	tracker.Stop()

	resource, exceeded := tracker.Check()
	if exceeded {
		t.Errorf("expected no budget exceeded for inactive tracker, got resource %v", resource)
	}

	if resource != "" {
		t.Errorf("expected empty resource for inactive tracker, got %v", resource)
	}
}

func TestTrackerCheckCanceled(t *testing.T) {
	budget := Budget{WallClock: 1 * time.Hour}
	tracker := NewTracker(budget)
	tracker.Cancel()

	resource, exceeded := tracker.Check()
	if exceeded {
		t.Errorf("expected no budget exceeded for canceled tracker, got resource %v", resource)
	}

	if resource != "" {
		t.Errorf("expected empty resource for canceled tracker, got %v", resource)
	}
}

func TestTrackerCheckAndCancel(t *testing.T) {
	budget := Budget{WallClock: 10 * time.Millisecond}
	tracker := NewTracker(budget)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Set the cancel function on the tracker so it can cancel the context
	tracker.cancelFunc = cancel

	time.Sleep(20 * time.Millisecond)

	resource, exceeded := tracker.CheckAndCancel(ctx)
	if !exceeded {
		t.Error("expected wall-clock budget to be exceeded")
	}

	if resource != ResourceTypeWallClock {
		t.Errorf("expected resource %v, got %v", ResourceTypeWallClock, resource)
	}

	time.Sleep(10 * time.Millisecond)

	if !tracker.IsCanceled() {
		t.Error("expected tracker to be canceled")
	}

	select {
	case <-ctx.Done():
	default:
		t.Error("expected context to be canceled")
	}
}

func TestTrackerCheckAndCancelNilContext(t *testing.T) {
	budget := Budget{WallClock: 10 * time.Millisecond}
	tracker := NewTracker(budget)

	time.Sleep(20 * time.Millisecond)

	resource, exceeded := tracker.CheckAndCancel(nil)
	if !exceeded {
		t.Error("expected wall-clock budget to be exceeded")
	}

	if resource != ResourceTypeWallClock {
		t.Errorf("expected resource %v, got %v", ResourceTypeWallClock, resource)
	}

	if !tracker.IsCanceled() {
		t.Error("expected tracker to be canceled even with nil context")
	}
}

func TestTrackerCancel(t *testing.T) {
	tracker := NewTracker(Budget{})

	if tracker.IsCanceled() {
		t.Error("expected tracker to not be canceled initially")
	}

	tracker.Cancel()

	if !tracker.IsCanceled() {
		t.Error("expected tracker to be canceled after Cancel")
	}

	if tracker.active {
		t.Error("expected tracker to be inactive after Cancel")
	}
}

func TestTrackerWallClockElapsed(t *testing.T) {
	budget := Budget{WallClock: 1 * time.Hour}
	tracker := NewTracker(budget)

	time.Sleep(10 * time.Millisecond)

	elapsed := tracker.WallClockElapsed()
	if elapsed < 10*time.Millisecond {
		t.Errorf("expected elapsed >= 10ms, got %v", elapsed)
	}
}

func TestTrackerWallClockElapsedInactive(t *testing.T) {
	budget := Budget{WallClock: 1 * time.Hour}
	tracker := NewTracker(budget)
	tracker.Stop()

	elapsed := tracker.WallClockElapsed()
	if elapsed != 0 {
		t.Errorf("expected elapsed = 0 for inactive tracker, got %v", elapsed)
	}
}

func TestTrackerCPUElapsed(t *testing.T) {
	budget := Budget{CPU: 1 * time.Hour}
	tracker := NewTracker(budget)

	// cpuTime() reads syscall.Getrusage's Utime, whose accounting
	// granularity varies by kernel/virtualization (coarser on some CI
	// runners than on a typical dev machine). A fixed iteration count
	// burns CPU for a duration that depends on host speed, and on a fast
	// or throttled host can finish under that granularity, reporting a
	// measured elapsed of exactly 0 - reproduced in CI, not just in
	// theory (see the commit that added this comment). Spin on wall-clock
	// time instead of an iteration count so this burns a guaranteed
	// minimum of real CPU regardless of host speed.
	var sum int64
	start := time.Now()
	for time.Since(start) < 100*time.Millisecond {
		for i := 0; i < 1000000; i++ {
			sum += int64(i)
		}
	}
	_ = sum

	elapsed := tracker.CPUElapsed()
	if elapsed <= 0 {
		t.Errorf("expected CPU elapsed > 0, got %v", elapsed)
	}
}

func TestTrackerCPUElapsedInactive(t *testing.T) {
	budget := Budget{CPU: 1 * time.Hour}
	tracker := NewTracker(budget)
	tracker.Stop()

	elapsed := tracker.CPUElapsed()
	if elapsed != 0 {
		t.Errorf("expected elapsed = 0 for inactive tracker, got %v", elapsed)
	}
}

func TestTrackerPeakMemory(t *testing.T) {
	budget := Budget{Memory: 1 * 1024 * 1024 * 1024}
	tracker := NewTracker(budget)

	bigSlice := make([]byte, 1024)
	_ = bigSlice

	tracker.UpdateMemory()

	peak := tracker.PeakMemory()
	if peak <= 0 {
		t.Errorf("expected peak memory > 0, got %v", peak)
	}
}

func TestTrackerCurrentMemory(t *testing.T) {
	budget := Budget{Memory: 1 * 1024 * 1024 * 1024}
	tracker := NewTracker(budget)

	current := tracker.CurrentMemory()
	if current <= 0 {
		t.Errorf("expected current memory > 0, got %v", current)
	}
}

func TestTrackerUpdateMemory(t *testing.T) {
	budget := Budget{Memory: 1 * 1024 * 1024 * 1024}
	tracker := NewTracker(budget)

	bigSlice := make([]byte, 1024)
	_ = bigSlice

	tracker.UpdateMemory()

	peak := tracker.PeakMemory()
	if peak <= 0 {
		t.Errorf("expected peak memory > 0 after update, got %v", peak)
	}
}

func TestBudgetExceededError(t *testing.T) {
	tests := []struct {
		name     string
		resource ResourceType
		budget   Budget
		actual   time.Duration
		memory   int64
		expected string
	}{
		{
			name:     "wallclock",
			resource: ResourceTypeWallClock,
			budget:   Budget{WallClock: 10 * time.Second},
			actual:   20 * time.Second,
			memory:   0,
			expected: "budget exceeded: wall-clock time 20s exceeded limit 10s",
		},
		{
			name:     "cpu",
			resource: ResourceTypeCPU,
			budget:   Budget{CPU: 10 * time.Second},
			actual:   20 * time.Second,
			memory:   0,
			expected: "budget exceeded: CPU time 20s exceeded limit 10s",
		},
		{
			name:     "memory",
			resource: ResourceTypeMemory,
			budget:   Budget{Memory: 1024},
			actual:   0,
			memory:   2048,
			expected: "budget exceeded: memory 2048 bytes exceeded limit 1024 bytes",
		},
		{
			name:     "unknown",
			resource: ResourceType("unknown"),
			budget:   Budget{},
			actual:   0,
			memory:   0,
			expected: "budget exceeded: unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &BudgetExceededError{
				Resource: tt.resource,
				Budget:   tt.budget,
				Actual:   tt.actual,
				Memory:   tt.memory,
			}

			if err.Error() != tt.expected {
				t.Errorf("Error() = %q, want %q", err.Error(), tt.expected)
			}
		})
	}
}

func TestIsBudgetExceeded(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "BudgetExceededError",
			err:  &BudgetExceededError{Resource: ResourceTypeWallClock, Budget: Budget{}},
			want: true,
		},
		{
			name: "wrapped BudgetExceededError",
			err:  fmt.Errorf("wrapped: %w", &BudgetExceededError{Resource: ResourceTypeCPU, Budget: Budget{}}),
			want: true,
		},
		{
			name: "standard error",
			err:  errors.New("standard error"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsBudgetExceeded(tt.err)
			if got != tt.want {
				t.Errorf("IsBudgetExceeded() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetBudgetExceeded(t *testing.T) {
	budgetErr := &BudgetExceededError{
		Resource: ResourceTypeWallClock,
		Budget:   Budget{WallClock: 10 * time.Second},
		Actual:   20 * time.Second,
		Memory:   0,
	}

	got := GetBudgetExceeded(budgetErr)
	if got == nil {
		t.Error("expected non-nil BudgetExceededError")
	}

	if got.Resource != ResourceTypeWallClock {
		t.Errorf("expected resource %v, got %v", ResourceTypeWallClock, got.Resource)
	}

	wrappedErr := fmt.Errorf("wrapped: %w", budgetErr)
	got = GetBudgetExceeded(wrappedErr)
	if got == nil {
		t.Error("expected non-nil BudgetExceededError for wrapped error")
	}

	got = GetBudgetExceeded(errors.New("standard error"))
	if got != nil {
		t.Error("expected nil for non-BudgetExceededError")
	}

	got = GetBudgetExceeded(nil)
	if got != nil {
		t.Error("expected nil for nil error")
	}
}

func TestManagerNewManager(t *testing.T) {
	manager := NewManager()

	if manager == nil {
		t.Fatal("expected non-nil manager")
	}

	if len(manager.trackers) != 0 {
		t.Errorf("expected empty trackers, got %d", len(manager.trackers))
	}
}

func TestManagerCreateTracker(t *testing.T) {
	manager := NewManager()
	budget := Budget{WallClock: 10 * time.Second}

	tracker := manager.CreateTracker(budget)

	if tracker == nil {
		t.Fatal("expected non-nil tracker")
	}

	if tracker.budget != budget {
		t.Errorf("expected budget %+v, got %+v", budget, tracker.budget)
	}

	if len(manager.trackers) != 1 {
		t.Errorf("expected 1 tracker in manager, got %d", len(manager.trackers))
	}
}

func TestManagerCreateMultipleTrackers(t *testing.T) {
	manager := NewManager()

	tracker1 := manager.CreateTracker(Budget{WallClock: 10 * time.Second})
	tracker2 := manager.CreateTracker(Budget{CPU: 5 * time.Second})

	if len(manager.trackers) != 2 {
		t.Errorf("expected 2 trackers, got %d", len(manager.trackers))
	}

	if manager.trackers[0] != tracker1 {
		t.Error("expected first tracker to be tracker1")
	}

	if manager.trackers[1] != tracker2 {
		t.Error("expected second tracker to be tracker2")
	}
}

func TestManagerTotalWallClock(t *testing.T) {
	manager := NewManager()

	manager.CreateTracker(Budget{})
	time.Sleep(10 * time.Millisecond)
	manager.CreateTracker(Budget{})

	total := manager.TotalWallClock()

	if total < 10*time.Millisecond {
		t.Errorf("expected total >= 10ms, got %v", total)
	}
}

func TestManagerTotalCPU(t *testing.T) {
	manager := NewManager()

	manager.CreateTracker(Budget{})
	manager.CreateTracker(Budget{})

	// See TestTrackerCPUElapsed's comment above: a fixed iteration count
	// burns CPU for a duration that depends on host speed, and can finish
	// under cpuTime()'s OS accounting granularity on a fast or throttled
	// CI host, reporting a measured total of exactly 0. Spin on
	// wall-clock time instead so this burns a guaranteed minimum of real
	// CPU regardless of host speed.
	result := 0
	start := time.Now()
	for time.Since(start) < 100*time.Millisecond {
		for i := range 1000000 {
			result = i * i
		}
	}

	runtime.KeepAlive(result)

	total := manager.TotalCPU()

	if total <= 0 {
		t.Errorf("expected total CPU > 0, got %v", total)
	}
}

func TestManagerPeakMemory(t *testing.T) {
	manager := NewManager()

	tracker1 := manager.CreateTracker(Budget{})
	tracker2 := manager.CreateTracker(Budget{})

	bigSlice := make([]byte, 1024)
	_ = bigSlice

	tracker1.UpdateMemory()
	tracker2.UpdateMemory()

	peak := manager.PeakMemory()

	if peak <= 0 {
		t.Errorf("expected peak memory > 0, got %v", peak)
	}
}

func TestManagerCheckAll(t *testing.T) {
	manager := NewManager()

	manager.CreateTracker(Budget{WallClock: 10 * time.Millisecond})

	time.Sleep(20 * time.Millisecond)

	resource, exceeded := manager.CheckAll()

	if !exceeded {
		t.Error("expected at least one budget to be exceeded")
	}

	if resource != ResourceTypeWallClock {
		t.Errorf("expected resource %v, got %v", ResourceTypeWallClock, resource)
	}
}

func TestManagerCheckAllNoExceed(t *testing.T) {
	manager := NewManager()

	manager.CreateTracker(Budget{WallClock: 1 * time.Hour})
	manager.CreateTracker(Budget{CPU: 1 * time.Hour})

	resource, exceeded := manager.CheckAll()

	if exceeded {
		t.Errorf("expected no budget exceeded, got resource %v", resource)
	}

	if resource != "" {
		t.Errorf("expected empty resource, got %v", resource)
	}
}

func TestManagerCancelAll(t *testing.T) {
	manager := NewManager()

	tracker1 := manager.CreateTracker(Budget{})
	tracker2 := manager.CreateTracker(Budget{})

	manager.CancelAll()

	if !tracker1.IsCanceled() {
		t.Error("expected tracker1 to be canceled")
	}

	if !tracker2.IsCanceled() {
		t.Error("expected tracker2 to be canceled")
	}
}

func TestContextWithBudget(t *testing.T) {
	parent := context.Background()
	budget := Budget{WallClock: 100 * time.Millisecond}

	ctx, tracker, cancel := ContextWithBudget(parent, budget)
	defer cancel()

	if ctx == nil {
		t.Fatal("expected non-nil context")
	}

	if tracker == nil {
		t.Fatal("expected non-nil tracker")
	}

	if cancel == nil {
		t.Fatal("expected non-nil cancel function")
	}

	if ctx.Err() != nil {
		t.Errorf("expected context to be valid, got error: %v", ctx.Err())
	}

	time.Sleep(150 * time.Millisecond)

	time.Sleep(50 * time.Millisecond)

	select {
	case <-ctx.Done():
	default:
		t.Error("expected context to be canceled after budget exceeded")
	}
}

func TestContextWithBudgetCancel(t *testing.T) {
	parent := context.Background()
	budget := Budget{WallClock: 1 * time.Hour}

	ctx, tracker, cancel := ContextWithBudget(parent, budget)

	cancel()

	time.Sleep(10 * time.Millisecond)

	select {
	case <-ctx.Done():
	default:
		t.Error("expected context to be canceled")
	}

	if tracker.active {
		t.Error("expected tracker to be inactive after cancel")
	}
}

func TestWaitWithBudget(t *testing.T) {
	parent := context.Background()
	budget := Budget{WallClock: 50 * time.Millisecond}

	err := WaitWithBudget(parent, budget, func(ctx context.Context) error {
		select {
		case <-time.After(10 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestWaitWithBudgetExceeded(t *testing.T) {
	parent := context.Background()
	budget := Budget{WallClock: 50 * time.Millisecond}

	err := WaitWithBudget(parent, budget, func(ctx context.Context) error {
		select {
		case <-time.After(200 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	budgetErr := GetBudgetExceeded(err)
	if budgetErr == nil {
		t.Fatal("expected BudgetExceededError")
	}

	if budgetErr.Resource != ResourceTypeWallClock {
		t.Errorf("expected resource %v, got %v", ResourceTypeWallClock, budgetErr.Resource)
	}
}

func TestWaitWithBudgetContextCanceled(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	budget := Budget{WallClock: 1 * time.Hour}

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := WaitWithBudget(parent, budget, func(ctx context.Context) error {
		select {
		case <-time.After(200 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if IsBudgetExceeded(err) {
		t.Error("expected context error, not budget error")
	}
}

func TestCPUTime(t *testing.T) {
	cpu := cpuTime()
	if cpu < 0 {
		t.Errorf("expected cpuTime >= 0, got %v", cpu)
	}
}

func TestCurrentMemory(t *testing.T) {
	mem := currentMemory()
	if mem <= 0 {
		t.Errorf("expected currentMemory > 0, got %v", mem)
	}
}

func TestConcurrentTrackerAccess(t *testing.T) {
	manager := NewManager()
	budget := Budget{WallClock: 1 * time.Hour}

	for i := 0; i < 10; i++ {
		manager.CreateTracker(budget)
	}

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				manager.TotalWallClock()
				manager.TotalCPU()
				manager.PeakMemory()
				manager.CheckAll()
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestManagerConcurrentTrackerCreation(t *testing.T) {
	manager := NewManager()

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				manager.CreateTracker(Budget{WallClock: 1 * time.Hour})
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	if len(manager.trackers) != 1000 {
		t.Errorf("expected 1000 trackers, got %d", len(manager.trackers))
	}
}

func TestBudgetExceededErrorImplementsError(t *testing.T) {
	var _ error = &BudgetExceededError{
		Resource: ResourceTypeWallClock,
		Budget:   Budget{},
		Actual:   0,
		Memory:   0,
	}
}

func TestZeroBudget(t *testing.T) {
	budget := Budget{}
	tracker := NewTracker(budget)

	time.Sleep(10 * time.Millisecond)

	resource, exceeded := tracker.Check()
	if exceeded {
		t.Errorf("expected no budget exceeded for zero budget, got resource %v", resource)
	}
}

func TestTrackerStartResetsState(t *testing.T) {
	budget := Budget{WallClock: 10 * time.Millisecond}
	tracker := NewTracker(budget)

	time.Sleep(20 * time.Millisecond)
	tracker.Check()

	if !tracker.wallClockExceeded {
		t.Error("expected wallClockExceeded to be true")
	}

	tracker.Start()

	if tracker.wallClockExceeded {
		t.Error("expected wallClockExceeded to be reset to false after Start")
	}

	resource, exceeded := tracker.Check()
	if exceeded {
		t.Errorf("expected no budget exceeded after Start, got resource %v", resource)
	}
}

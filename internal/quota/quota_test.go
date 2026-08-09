package quota

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestResourceType(t *testing.T) {
	tests := []struct {
		resource ResourceType
		expected string
	}{
		{ResourceTypeCPU, "cpu"},
		{ResourceTypeMemory, "memory"},
		{ResourceTypeStorage, "storage"},
		{ResourceTypeNetwork, "network"},
		{ResourceTypeParallel, "parallel"},
	}

	for _, tt := range tests {
		t.Run(string(tt.expected), func(t *testing.T) {
			if string(tt.resource) != tt.expected {
				t.Errorf("ResourceType = %v, want %v", string(tt.resource), tt.expected)
			}
		})
	}
}

func TestQuotaIsZero(t *testing.T) {
	tests := []struct {
		name     string
		quota    Quota
		expected bool
	}{
		{"zero quota", Quota{}, true},
		{"cpu only", Quota{CPU: 100}, false},
		{"memory only", Quota{Memory: 1024}, false},
		{"storage only", Quota{Storage: 1024}, false},
		{"network only", Quota{Network: 1024}, false},
		{"parallel only", Quota{MaxParallel: 1}, false},
		{"priority only", Quota{Priority: 1}, false},
		{"fairness only", Quota{FairnessWeight: 1.0}, false},
		{"all set", Quota{CPU: 100, Memory: 1024, Storage: 1024, Network: 1024, MaxParallel: 1, Priority: 1, FairnessWeight: 1.0}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.quota.IsZero()
			if got != tt.expected {
				t.Errorf("IsZero() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestNewTenantQuota(t *testing.T) {
	expectedDefault := Quota{CPU: 50, Memory: 1024}
	quotas := NewTenantQuota(expectedDefault)

	if quotas == nil {
		t.Fatal("expected non-nil TenantQuota")
	}

	if len(quotas.quotas) != 0 {
		t.Errorf("expected empty quotas map, got %d", len(quotas.quotas))
	}

	if quotas.defaultQ != expectedDefault {
		t.Errorf("expected default %+v, got %+v", expectedDefault, quotas.defaultQ)
	}
}

func TestTenantQuotaSetGet(t *testing.T) {
	quotas := NewTenantQuota(Quota{})
	tenant1 := TenantID("tenant-1")
	quota1 := Quota{CPU: 100, Memory: 2048}

	quotas.SetQuota(tenant1, quota1)
	got := quotas.GetQuota(tenant1)

	if got != quota1 {
		t.Errorf("expected quota %+v, got %+v", quota1, got)
	}
}

func TestTenantQuotaGetDefault(t *testing.T) {
	expectedDefault := Quota{CPU: 50, Memory: 1024}
	quotas := NewTenantQuota(expectedDefault)
	tenant1 := TenantID("tenant-1")

	got := quotas.GetQuota(tenant1)

	if got != expectedDefault {
		t.Errorf("expected default quota %+v, got %+v", expectedDefault, got)
	}
}

func TestTenantQuotaDelete(t *testing.T) {
	quotas := NewTenantQuota(Quota{})
	tenant1 := TenantID("tenant-1")
	quota1 := Quota{CPU: 100}

	quotas.SetQuota(tenant1, quota1)
	quotas.DeleteQuota(tenant1)

	got := quotas.GetQuota(tenant1)
	if !got.IsZero() {
		t.Errorf("expected zero quota after delete, got %+v", got)
	}
}

func TestNewTenantStats(t *testing.T) {
	stats := NewTenantStats()

	if stats == nil {
		t.Fatal("expected non-nil TenantStats")
	}

	if !stats.LastUpdated().Before(time.Now().Add(time.Second)) {
		t.Error("expected LastUpdated to be recent")
	}

	if stats.CPUUsed() != 0 {
		t.Errorf("expected CPUUsed = 0, got %d", stats.CPUUsed())
	}

	if stats.MemoryUsed() != 0 {
		t.Errorf("expected MemoryUsed = 0, got %d", stats.MemoryUsed())
	}

	if stats.StorageUsed() != 0 {
		t.Errorf("expected StorageUsed = 0, got %d", stats.StorageUsed())
	}

	if stats.NetworkUsed() != 0 {
		t.Errorf("expected NetworkUsed = 0, got %d", stats.NetworkUsed())
	}

	if stats.ParallelUsed() != 0 {
		t.Errorf("expected ParallelUsed = 0, got %d", stats.ParallelUsed())
	}
}

func TestTenantStatsUpdates(t *testing.T) {
	stats := NewTenantStats()

	stats.UpdateCPU(100)
	if stats.CPUUsed() != 100 {
		t.Errorf("expected CPUUsed = 100, got %d", stats.CPUUsed())
	}

	stats.UpdateMemory(2048)
	if stats.MemoryUsed() != 2048 {
		t.Errorf("expected MemoryUsed = 2048, got %d", stats.MemoryUsed())
	}

	stats.UpdateStorage(4096)
	if stats.StorageUsed() != 4096 {
		t.Errorf("expected StorageUsed = 4096, got %d", stats.StorageUsed())
	}

	stats.UpdateNetwork(512)
	if stats.NetworkUsed() != 512 {
		t.Errorf("expected NetworkUsed = 512, got %d", stats.NetworkUsed())
	}

	stats.UpdateParallel(5)
	if stats.ParallelUsed() != 5 {
		t.Errorf("expected ParallelUsed = 5, got %d", stats.ParallelUsed())
	}
}

func TestNewFairScheduler(t *testing.T) {
	quotas := NewTenantQuota(Quota{})
	scheduler := NewFairScheduler(quotas)

	if scheduler == nil {
		t.Fatal("expected non-nil FairScheduler")
	}

	if scheduler.quotas != quotas {
		t.Error("expected quotas to be set")
	}

	if len(scheduler.stats) != 0 {
		t.Errorf("expected empty stats, got %d", len(scheduler.stats))
	}
}

func TestFairSchedulerCanSchedule(t *testing.T) {
	quotas := NewTenantQuota(Quota{})
	tenant1 := TenantID("tenant-1")
	quotas.SetQuota(tenant1, Quota{CPU: 1000, Memory: 4096, MaxParallel: 10})

	scheduler := NewFairScheduler(quotas)

	// Test CPU quota
	canSchedule, err := scheduler.CanSchedule(tenant1, ResourceTypeCPU, 500)
	if !canSchedule {
		t.Errorf("expected to be able to schedule 500 CPU, got error: %v", err)
	}

	// Schedule the CPU
	scheduler.Schedule(tenant1, ResourceTypeCPU, 500)

	// Try to schedule more than quota
	canSchedule, err = scheduler.CanSchedule(tenant1, ResourceTypeCPU, 600)
	if canSchedule {
		t.Error("expected not to be able to schedule 600 more CPU")
	}
	if err == nil {
		t.Error("expected error when exceeding quota")
	}
	if quotaErr, ok := err.(*QuotaExceededError); !ok {
		t.Errorf("expected QuotaExceededError, got %T", err)
	} else {
		if quotaErr.Tenant != tenant1 {
			t.Errorf("expected tenant %s, got %s", tenant1, quotaErr.Tenant)
		}
		if quotaErr.Resource != ResourceTypeCPU {
			t.Errorf("expected resource %s, got %s", ResourceTypeCPU, quotaErr.Resource)
		}
	}

	// Test parallel quota
	for i := 0; i < 10; i++ {
		canSchedule, err = scheduler.CanSchedule(tenant1, ResourceTypeParallel, 1)
		if !canSchedule {
			t.Errorf("expected to be able to schedule parallel %d, got error: %v", i+1, err)
		}
		scheduler.Schedule(tenant1, ResourceTypeParallel, 1)
	}

	// Try to schedule one more parallel
	canSchedule, err = scheduler.CanSchedule(tenant1, ResourceTypeParallel, 1)
	if canSchedule {
		t.Error("expected not to be able to schedule more parallel")
	}
}

func TestFairSchedulerScheduleAndRelease(t *testing.T) {
	quotas := NewTenantQuota(Quota{})
	tenant1 := TenantID("tenant-1")
	quotas.SetQuota(tenant1, Quota{CPU: 1000})

	scheduler := NewFairScheduler(quotas)

	// Schedule some CPU
	err := scheduler.Schedule(tenant1, ResourceTypeCPU, 500)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Check usage
	info := scheduler.GetTenantInfo(tenant1)
	if info.CPUUsed != 500 {
		t.Errorf("expected CPUUsed = 500, got %d", info.CPUUsed)
	}

	// Release some CPU
	scheduler.Release(tenant1, ResourceTypeCPU, 200)

	// Check usage after release
	info = scheduler.GetTenantInfo(tenant1)
	if info.CPUUsed != 300 {
		t.Errorf("expected CPUUsed = 300 after release, got %d", info.CPUUsed)
	}
}

func TestFairSchedulerNoQuota(t *testing.T) {
	quotas := NewTenantQuota(Quota{})
	tenant1 := TenantID("tenant-1")
	// No quota set for tenant1

	scheduler := NewFairScheduler(quotas)

	// Should be able to schedule without quota
	canSchedule, err := scheduler.CanSchedule(tenant1, ResourceTypeCPU, 10000)
	if !canSchedule {
		t.Errorf("expected to be able to schedule without quota, got error: %v", err)
	}
}

func TestFairSchedulerPriority(t *testing.T) {
	quotas := NewTenantQuota(Quota{})
	tenant1 := TenantID("tenant-1")
	quotas.SetQuota(tenant1, Quota{Priority: 10})

	scheduler := NewFairScheduler(quotas)

	priority := scheduler.GetPriority(tenant1)
	if priority != 10 {
		t.Errorf("expected priority 10, got %d", priority)
	}
}

func TestFairSchedulerFairnessWeight(t *testing.T) {
	quotas := NewTenantQuota(Quota{})
	tenant1 := TenantID("tenant-1")
	quotas.SetQuota(tenant1, Quota{FairnessWeight: 2.5})

	scheduler := NewFairScheduler(quotas)

	weight := scheduler.GetFairnessWeight(tenant1)
	if weight != 2.5 {
		t.Errorf("expected fairness weight 2.5, got %f", weight)
	}
}

func TestFairSchedulerGetTenantInfo(t *testing.T) {
	quotas := NewTenantQuota(Quota{})
	tenant1 := TenantID("tenant-1")
	quotas.SetQuota(tenant1, Quota{CPU: 1000, Memory: 4096, Priority: 5, FairnessWeight: 1.5})

	scheduler := NewFairScheduler(quotas)
	scheduler.Schedule(tenant1, ResourceTypeCPU, 300)
	scheduler.Schedule(tenant1, ResourceTypeMemory, 1024)

	info := scheduler.GetTenantInfo(tenant1)

	if info.Tenant != tenant1 {
		t.Errorf("expected tenant %s, got %s", tenant1, info.Tenant)
	}
	if info.Quota.CPU != 1000 {
		t.Errorf("expected CPU quota 1000, got %d", info.Quota.CPU)
	}
	if info.CPUUsed != 300 {
		t.Errorf("expected CPUUsed 300, got %d", info.CPUUsed)
	}
	if info.MemoryUsed != 1024 {
		t.Errorf("expected MemoryUsed 1024, got %d", info.MemoryUsed)
	}
	if info.Priority != 5 {
		t.Errorf("expected priority 5, got %d", info.Priority)
	}
	if info.FairnessWeight != 1.5 {
		t.Errorf("expected fairness weight 1.5, got %f", info.FairnessWeight)
	}
}

func TestFairSchedulerGetAllTenantsInfo(t *testing.T) {
	quotas := NewTenantQuota(Quota{})
	tenant1 := TenantID("tenant-1")
	tenant2 := TenantID("tenant-2")
	quotas.SetQuota(tenant1, Quota{CPU: 1000})
	quotas.SetQuota(tenant2, Quota{CPU: 2000})

	scheduler := NewFairScheduler(quotas)
	scheduler.Schedule(tenant1, ResourceTypeCPU, 100)
	scheduler.Schedule(tenant2, ResourceTypeCPU, 200)

	infos := scheduler.GetAllTenantsInfo()
	if len(infos) != 2 {
		t.Errorf("expected 2 tenants, got %d", len(infos))
	}

	// Find tenant1 info
	found := false
	for _, info := range infos {
		if info.Tenant == tenant1 {
			found = true
			if info.CPUUsed != 100 {
				t.Errorf("expected tenant1 CPUUsed = 100, got %d", info.CPUUsed)
			}
			break
		}
	}
	if !found {
		t.Error("tenant1 not found in info")
	}
}

func TestWithTenant(t *testing.T) {
	ctx := context.Background()
	tenant1 := TenantID("tenant-1")

	ctx = WithTenant(ctx, tenant1)
	got, ok := FromContext(ctx)
	if !ok {
		t.Error("expected to find tenant in context")
	}
	if got != tenant1 {
		t.Errorf("expected tenant %s, got %s", tenant1, got)
	}
}

func TestFromContextNotFound(t *testing.T) {
	ctx := context.Background()
	_, ok := FromContext(ctx)
	if ok {
		t.Error("expected not to find tenant in empty context")
	}
}

func TestQuotaExceededError(t *testing.T) {
	err := &QuotaExceededError{
		Tenant:   TenantID("tenant-1"),
		Resource: ResourceTypeCPU,
		Used:     1500,
		Limit:    1000,
	}

	expected := "tenant tenant-1 exceeded cpu quota: used 1500, limit 1000"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestConcurrentAccess(t *testing.T) {
	quotas := NewTenantQuota(Quota{CPU: 10000})
	scheduler := NewFairScheduler(quotas)

	var wg sync.WaitGroup
	numGoroutines := 100

	// Concurrent scheduling
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			tenant := TenantID(fmt.Sprintf("tenant-%d", id))
			quotas.SetQuota(tenant, Quota{CPU: 10000})

			for j := 0; j < 10; j++ {
				scheduler.Schedule(tenant, ResourceTypeCPU, 10)
				scheduler.Release(tenant, ResourceTypeCPU, 5)
				scheduler.GetPriority(tenant)
				scheduler.GetFairnessWeight(tenant)
				scheduler.GetTenantInfo(tenant)
			}
		}(i)
	}

	wg.Wait()

	// Verify we can still use the scheduler
	quotas.SetQuota(TenantID("final-test"), Quota{CPU: 100})
	canSchedule, _ := scheduler.CanSchedule(TenantID("final-test"), ResourceTypeCPU, 50)
	if !canSchedule {
		t.Error("expected scheduler to work after concurrent access")
	}
}

func TestReleaseBelowZero(t *testing.T) {
	quotas := NewTenantQuota(Quota{})
	tenant1 := TenantID("tenant-1")
	quotas.SetQuota(tenant1, Quota{CPU: 1000})

	scheduler := NewFairScheduler(quotas)

	// Schedule some CPU
	scheduler.Schedule(tenant1, ResourceTypeCPU, 100)

	// Release more than scheduled
	scheduler.Release(tenant1, ResourceTypeCPU, 200)

	// Should not go below zero
	info := scheduler.GetTenantInfo(tenant1)
	if info.CPUUsed != 0 {
		t.Errorf("expected CPUUsed = 0 after over-release, got %d", info.CPUUsed)
	}
}

func TestScheduleAllResourceTypes(t *testing.T) {
	quotas := NewTenantQuota(Quota{})
	tenant1 := TenantID("tenant-1")
	quotas.SetQuota(tenant1, Quota{
		CPU:         1000,
		Memory:      1024 * 1024,
		Storage:     1024 * 1024,
		Network:     1024 * 1024,
		MaxParallel: 10,
	})

	scheduler := NewFairScheduler(quotas)

	// Test scheduling all resource types
	scheduler.Schedule(tenant1, ResourceTypeCPU, 500)
	scheduler.Schedule(tenant1, ResourceTypeMemory, 512*1024)
	scheduler.Schedule(tenant1, ResourceTypeStorage, 512*1024)
	scheduler.Schedule(tenant1, ResourceTypeNetwork, 512*1024)
	scheduler.Schedule(tenant1, ResourceTypeParallel, 5)

	info := scheduler.GetTenantInfo(tenant1)
	if info.CPUUsed != 500 {
		t.Errorf("expected CPUUsed = 500, got %d", info.CPUUsed)
	}
	if info.MemoryUsed != 512*1024 {
		t.Errorf("expected MemoryUsed = %d, got %d", 512*1024, info.MemoryUsed)
	}
	if info.StorageUsed != 512*1024 {
		t.Errorf("expected StorageUsed = %d, got %d", 512*1024, info.StorageUsed)
	}
	if info.NetworkUsed != 512*1024 {
		t.Errorf("expected NetworkUsed = %d, got %d", 512*1024, info.NetworkUsed)
	}
	if info.ParallelUsed != 5 {
		t.Errorf("expected ParallelUsed = 5, got %d", info.ParallelUsed)
	}
}

func TestReleaseAllResourceTypes(t *testing.T) {
	quotas := NewTenantQuota(Quota{})
	tenant1 := TenantID("tenant-1")
	quotas.SetQuota(tenant1, Quota{
		CPU:         1000,
		Memory:      1024 * 1024,
		Storage:     1024 * 1024,
		Network:     1024 * 1024,
		MaxParallel: 10,
	})

	scheduler := NewFairScheduler(quotas)

	// Schedule resources
	scheduler.Schedule(tenant1, ResourceTypeCPU, 500)
	scheduler.Schedule(tenant1, ResourceTypeMemory, 512*1024)
	scheduler.Schedule(tenant1, ResourceTypeStorage, 512*1024)
	scheduler.Schedule(tenant1, ResourceTypeNetwork, 512*1024)
	scheduler.Schedule(tenant1, ResourceTypeParallel, 5)

	// Release resources
	scheduler.Release(tenant1, ResourceTypeCPU, 200)
	scheduler.Release(tenant1, ResourceTypeMemory, 256*1024)
	scheduler.Release(tenant1, ResourceTypeStorage, 256*1024)
	scheduler.Release(tenant1, ResourceTypeNetwork, 256*1024)
	scheduler.Release(tenant1, ResourceTypeParallel, 2)

	info := scheduler.GetTenantInfo(tenant1)
	if info.CPUUsed != 300 {
		t.Errorf("expected CPUUsed = 300, got %d", info.CPUUsed)
	}
	if info.MemoryUsed != 256*1024 {
		t.Errorf("expected MemoryUsed = %d, got %d", 256*1024, info.MemoryUsed)
	}
	if info.StorageUsed != 256*1024 {
		t.Errorf("expected StorageUsed = %d, got %d", 256*1024, info.StorageUsed)
	}
	if info.NetworkUsed != 256*1024 {
		t.Errorf("expected NetworkUsed = %d, got %d", 256*1024, info.NetworkUsed)
	}
	if info.ParallelUsed != 3 {
		t.Errorf("expected ParallelUsed = 3, got %d", info.ParallelUsed)
	}
}

func TestCanScheduleAllResourceTypes(t *testing.T) {
	quotas := NewTenantQuota(Quota{})
	tenant1 := TenantID("tenant-1")
	quotas.SetQuota(tenant1, Quota{
		CPU:         1000,
		Memory:      1024 * 1024,
		Storage:     1024 * 1024,
		Network:     1024 * 1024,
		MaxParallel: 10,
	})

	scheduler := NewFairScheduler(quotas)

	// Test CanSchedule for all resource types
	canSchedule, err := scheduler.CanSchedule(tenant1, ResourceTypeCPU, 500)
	if !canSchedule {
		t.Errorf("expected to schedule CPU: %v", err)
	}

	canSchedule, err = scheduler.CanSchedule(tenant1, ResourceTypeMemory, 512*1024)
	if !canSchedule {
		t.Errorf("expected to schedule Memory: %v", err)
	}

	canSchedule, err = scheduler.CanSchedule(tenant1, ResourceTypeStorage, 512*1024)
	if !canSchedule {
		t.Errorf("expected to schedule Storage: %v", err)
	}

	canSchedule, err = scheduler.CanSchedule(tenant1, ResourceTypeNetwork, 512*1024)
	if !canSchedule {
		t.Errorf("expected to schedule Network: %v", err)
	}

	canSchedule, err = scheduler.CanSchedule(tenant1, ResourceTypeParallel, 5)
	if !canSchedule {
		t.Errorf("expected to schedule Parallel: %v", err)
	}
}

func TestCanScheduleExceedsQuotaAllTypes(t *testing.T) {
	quotas := NewTenantQuota(Quota{})
	tenant1 := TenantID("tenant-1")
	quotas.SetQuota(tenant1, Quota{
		CPU:         100,
		Memory:      100,
		Storage:     100,
		Network:     100,
		MaxParallel: 2,
	})

	scheduler := NewFairScheduler(quotas)

	// Test exceeding quota for all resource types
	canSchedule, err := scheduler.CanSchedule(tenant1, ResourceTypeCPU, 200)
	if canSchedule {
		t.Error("expected not to schedule CPU exceeding quota")
	}
	if err == nil {
		t.Error("expected error for CPU exceeding quota")
	}

	canSchedule, err = scheduler.CanSchedule(tenant1, ResourceTypeMemory, 200)
	if canSchedule {
		t.Error("expected not to schedule Memory exceeding quota")
	}

	canSchedule, err = scheduler.CanSchedule(tenant1, ResourceTypeStorage, 200)
	if canSchedule {
		t.Error("expected not to schedule Storage exceeding quota")
	}

	canSchedule, err = scheduler.CanSchedule(tenant1, ResourceTypeNetwork, 200)
	if canSchedule {
		t.Error("expected not to schedule Network exceeding quota")
	}

	canSchedule, err = scheduler.CanSchedule(tenant1, ResourceTypeParallel, 5)
	if canSchedule {
		t.Error("expected not to schedule Parallel exceeding quota")
	}
}

func TestQuotaZeroValues(t *testing.T) {
	quotas := NewTenantQuota(Quota{})

	// Test zero quota
	zeroQuota := quotas.GetQuota(TenantID("nonexistent"))
	if !zeroQuota.IsZero() {
		t.Errorf("expected zero quota for nonexistent tenant, got %+v", zeroQuota)
	}

	// Test setting and getting zero quota explicitly
	quotas.SetQuota(TenantID("tenant-zero"), Quota{})
	gotQuota := quotas.GetQuota(TenantID("tenant-zero"))
	if !gotQuota.IsZero() {
		t.Errorf("expected zero quota, got %+v", gotQuota)
	}
}

// Package quota provides tenant-level resource quotas, priorities, and fairness
// for the pipeline scheduler and distributed execution system.
package quota

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
)

// ResourceType represents the type of resource being quotas.
type ResourceType string

const (
	ResourceTypeCPU      ResourceType = "cpu"
	ResourceTypeMemory   ResourceType = "memory"
	ResourceTypeStorage  ResourceType = "storage"
	ResourceTypeNetwork  ResourceType = "network"
	ResourceTypeParallel ResourceType = "parallel"
)

// Quota represents resource limits for a tenant.
type Quota struct {
	// CPU quota in milliseconds per second (0-1000 = 0-100%)
	CPU int64 `json:"cpu"`
	// Memory quota in bytes
	Memory int64 `json:"memory"`
	// Storage quota in bytes
	Storage int64 `json:"storage"`
	// Network quota in bytes per second
	Network int64 `json:"network"`
	// MaxParallel is the maximum number of concurrent executions
	MaxParallel int `json:"max_parallel"`
	// Priority is the scheduling priority (higher = more important)
	Priority int `json:"priority"`
	// FairnessWeight is the fairness weight for proportional sharing
	FairnessWeight float64 `json:"fairness_weight"`
}

// IsZero returns true if all quota values are zero.
func (q Quota) IsZero() bool {
	return q.CPU == 0 && q.Memory == 0 && q.Storage == 0 &&
		q.Network == 0 && q.MaxParallel == 0 && q.Priority == 0 && q.FairnessWeight == 0
}

// TenantID is a unique identifier for a tenant.
type TenantID string

// TenantQuota maps tenant IDs to their quotas.
type TenantQuota struct {
	mu       sync.RWMutex
	quotas   map[TenantID]Quota
	defaultQ Quota
}

// NewTenantQuota creates a new TenantQuota with optional default quota.
func NewTenantQuota(defaultQuota Quota) *TenantQuota {
	return &TenantQuota{
		quotas:   make(map[TenantID]Quota),
		defaultQ: defaultQuota,
	}
}

// SetQuota sets the quota for a specific tenant.
func (tq *TenantQuota) SetQuota(tenant TenantID, quota Quota) {
	tq.mu.Lock()
	defer tq.mu.Unlock()
	tq.quotas[tenant] = quota
}

// GetQuota returns the quota for a specific tenant, or the default if not set.
func (tq *TenantQuota) GetQuota(tenant TenantID) Quota {
	tq.mu.RLock()
	defer tq.mu.RUnlock()
	if q, ok := tq.quotas[tenant]; ok {
		return q
	}
	return tq.defaultQ
}

// DeleteQuota removes the quota for a specific tenant.
func (tq *TenantQuota) DeleteQuota(tenant TenantID) {
	tq.mu.Lock()
	defer tq.mu.Unlock()
	delete(tq.quotas, tenant)
}

// TenantStats tracks current resource usage for a tenant.
type TenantStats struct {
	mu           sync.RWMutex
	cpuUsed      int64
	memoryUsed   int64
	storageUsed  int64
	networkUsed  int64
	parallelUsed int
	lastUpdated  time.Time
}

// NewTenantStats creates a new TenantStats.
func NewTenantStats() *TenantStats {
	return &TenantStats{
		lastUpdated: time.Now(),
	}
}

// UpdateCPU updates the CPU usage.
func (ts *TenantStats) UpdateCPU(used int64) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.cpuUsed = used
	ts.lastUpdated = time.Now()
}

// UpdateMemory updates the memory usage.
func (ts *TenantStats) UpdateMemory(used int64) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.memoryUsed = used
	ts.lastUpdated = time.Now()
}

// UpdateStorage updates the storage usage.
func (ts *TenantStats) UpdateStorage(used int64) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.storageUsed = used
	ts.lastUpdated = time.Now()
}

// UpdateNetwork updates the network usage.
func (ts *TenantStats) UpdateNetwork(used int64) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.networkUsed = used
	ts.lastUpdated = time.Now()
}

// UpdateParallel updates the parallel execution count.
func (ts *TenantStats) UpdateParallel(used int) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.parallelUsed = used
	ts.lastUpdated = time.Now()
}

// CPUUsed returns the current CPU usage.
func (ts *TenantStats) CPUUsed() int64 {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.cpuUsed
}

// MemoryUsed returns the current memory usage.
func (ts *TenantStats) MemoryUsed() int64 {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.memoryUsed
}

// StorageUsed returns the current storage usage.
func (ts *TenantStats) StorageUsed() int64 {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.storageUsed
}

// NetworkUsed returns the current network usage.
func (ts *TenantStats) NetworkUsed() int64 {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.networkUsed
}

// ParallelUsed returns the current parallel execution count.
func (ts *TenantStats) ParallelUsed() int {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.parallelUsed
}

// LastUpdated returns the last update time.
func (ts *TenantStats) LastUpdated() time.Time {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.lastUpdated
}

// SchedulerStats tracks statistics for the fairness scheduler.
type SchedulerStats struct {
	mu            sync.RWMutex
	totalCPU      int64
	totalMemory   int64
	totalParallel int
}

// QuotaExceededError is returned when a tenant exceeds their quota.
type QuotaExceededError struct {
	Tenant   TenantID
	Resource ResourceType
	Used     int64
	Limit    int64
}

func (e *QuotaExceededError) Error() string {
	return fmt.Sprintf("tenant %s exceeded %s quota: used %d, limit %d",
		e.Tenant, e.Resource, e.Used, e.Limit)
}

// FairScheduler provides fairness-aware scheduling based on tenant quotas and priorities.
type FairScheduler struct {
	// accountingMu serializes admission checks with reservations and releases.
	accountingMu sync.Mutex
	quotas       *TenantQuota
	stats        map[TenantID]*TenantStats
	global       *SchedulerStats
	mu           sync.RWMutex
}

// NewFairScheduler creates a new FairScheduler.
func NewFairScheduler(quotas *TenantQuota) *FairScheduler {
	return &FairScheduler{
		quotas: quotas,
		stats:  make(map[TenantID]*TenantStats),
		global: &SchedulerStats{},
	}
}

// CanSchedule checks if a tenant can schedule a new execution based on their quota.
func (fs *FairScheduler) CanSchedule(tenant TenantID, resource ResourceType, requested int64) (bool, error) {
	fs.accountingMu.Lock()
	defer fs.accountingMu.Unlock()
	return fs.canSchedule(tenant, resource, requested)
}

func (fs *FairScheduler) canSchedule(tenant TenantID, resource ResourceType, requested int64) (bool, error) {
	if requested < 0 {
		return false, fmt.Errorf("resource reservation must not be negative")
	}
	quota := fs.quotas.GetQuota(tenant)

	stats := fs.getOrCreateStats(tenant)

	var used, limit int64
	switch resource {
	case ResourceTypeCPU:
		used = stats.CPUUsed()
		limit = quota.CPU
	case ResourceTypeMemory:
		used = stats.MemoryUsed()
		limit = quota.Memory
	case ResourceTypeStorage:
		used = stats.StorageUsed()
		limit = quota.Storage
	case ResourceTypeNetwork:
		used = stats.NetworkUsed()
		limit = quota.Network
	case ResourceTypeParallel:
		used = int64(stats.ParallelUsed())
		limit = int64(quota.MaxParallel)
	default:
		return true, nil
	}

	if requested > math.MaxInt64-used {
		return false, fmt.Errorf("resource reservation overflows accounting")
	}
	if resource == ResourceTypeParallel && requested > int64(int(^uint(0)>>1))-used {
		return false, fmt.Errorf("parallel reservation overflows accounting")
	}

	if limit <= 0 {
		return true, nil // No limit set
	}

	if used+requested > limit {
		return false, &QuotaExceededError{
			Tenant:   tenant,
			Resource: resource,
			Used:     used + requested,
			Limit:    limit,
		}
	}

	return true, nil
}

// Schedule attempts to schedule a resource for a tenant.
func (fs *FairScheduler) Schedule(tenant TenantID, resource ResourceType, amount int64) error {
	fs.accountingMu.Lock()
	defer fs.accountingMu.Unlock()
	canSchedule, err := fs.canSchedule(tenant, resource, amount)
	if !canSchedule {
		return err
	}

	stats := fs.getOrCreateStats(tenant)

	switch resource {
	case ResourceTypeCPU:
		stats.UpdateCPU(stats.CPUUsed() + amount)
		fs.global.mu.Lock()
		fs.global.totalCPU += amount
		fs.global.mu.Unlock()
	case ResourceTypeMemory:
		stats.UpdateMemory(stats.MemoryUsed() + amount)
		fs.global.mu.Lock()
		fs.global.totalMemory += amount
		fs.global.mu.Unlock()
	case ResourceTypeStorage:
		stats.UpdateStorage(stats.StorageUsed() + amount)
	case ResourceTypeNetwork:
		stats.UpdateNetwork(stats.NetworkUsed() + amount)
	case ResourceTypeParallel:
		stats.UpdateParallel(stats.ParallelUsed() + int(amount))
		fs.global.mu.Lock()
		fs.global.totalParallel += int(amount)
		fs.global.mu.Unlock()
	}

	return nil
}

// Release releases resources for a tenant.
func (fs *FairScheduler) Release(tenant TenantID, resource ResourceType, amount int64) {
	if amount <= 0 {
		return
	}
	fs.accountingMu.Lock()
	defer fs.accountingMu.Unlock()
	stats := fs.getOrCreateStats(tenant)

	switch resource {
	case ResourceTypeCPU:
		released := min(stats.CPUUsed(), amount)
		newCPU := stats.CPUUsed() - released
		if newCPU < 0 {
			newCPU = 0
		}
		stats.UpdateCPU(newCPU)
		fs.global.mu.Lock()
		fs.global.totalCPU -= released
		if fs.global.totalCPU < 0 {
			fs.global.totalCPU = 0
		}
		fs.global.mu.Unlock()
	case ResourceTypeMemory:
		released := min(stats.MemoryUsed(), amount)
		newMemory := stats.MemoryUsed() - released
		if newMemory < 0 {
			newMemory = 0
		}
		stats.UpdateMemory(newMemory)
		fs.global.mu.Lock()
		fs.global.totalMemory -= released
		if fs.global.totalMemory < 0 {
			fs.global.totalMemory = 0
		}
		fs.global.mu.Unlock()
	case ResourceTypeStorage:
		newStorage := stats.StorageUsed() - amount
		if newStorage < 0 {
			newStorage = 0
		}
		stats.UpdateStorage(newStorage)
	case ResourceTypeNetwork:
		newNetwork := stats.NetworkUsed() - amount
		if newNetwork < 0 {
			newNetwork = 0
		}
		stats.UpdateNetwork(newNetwork)
	case ResourceTypeParallel:
		released := min(int64(stats.ParallelUsed()), amount)
		newParallel := stats.ParallelUsed() - int(released)
		if newParallel < 0 {
			newParallel = 0
		}
		stats.UpdateParallel(newParallel)
		fs.global.mu.Lock()
		fs.global.totalParallel -= int(released)
		if fs.global.totalParallel < 0 {
			fs.global.totalParallel = 0
		}
		fs.global.mu.Unlock()
	}
}

// RestoreParallelUsage replaces concurrency accounting from durable open leases.
// Call before serving requests. Existing work is counted even if a configured
// limit has since been lowered; new admissions remain blocked until it drains.
func (fs *FairScheduler) RestoreParallelUsage(usage map[TenantID]int) error {
	fs.accountingMu.Lock()
	defer fs.accountingMu.Unlock()
	total := 0
	for _, count := range usage {
		if count < 0 || count > int(^uint(0)>>1)-total {
			return fmt.Errorf("invalid restored parallel usage")
		}
		total += count
	}
	fs.mu.Lock()
	for tenant, stats := range fs.stats {
		stats.UpdateParallel(usage[tenant])
	}
	for tenant, count := range usage {
		if _, ok := fs.stats[tenant]; !ok {
			stats := NewTenantStats()
			stats.UpdateParallel(count)
			fs.stats[tenant] = stats
		}
	}
	fs.mu.Unlock()
	fs.global.mu.Lock()
	fs.global.totalParallel = total
	fs.global.mu.Unlock()
	return nil
}

// GetPriority returns the priority for a tenant.
func (fs *FairScheduler) GetPriority(tenant TenantID) int {
	quota := fs.quotas.GetQuota(tenant)
	return quota.Priority
}

// GetFairnessWeight returns the fairness weight for a tenant.
func (fs *FairScheduler) GetFairnessWeight(tenant TenantID) float64 {
	quota := fs.quotas.GetQuota(tenant)
	return quota.FairnessWeight
}

// getOrCreateStats returns existing stats or creates new ones for a tenant.
func (fs *FairScheduler) getOrCreateStats(tenant TenantID) *TenantStats {
	fs.mu.RLock()
	stats, exists := fs.stats[tenant]
	fs.mu.RUnlock()

	if exists {
		return stats
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()
	// Double-check after acquiring write lock
	if stats, exists = fs.stats[tenant]; exists {
		return stats
	}

	stats = NewTenantStats()
	fs.stats[tenant] = stats
	return stats
}

// TenantInfo provides information about a tenant's quota and usage.
type TenantInfo struct {
	Tenant         TenantID
	Quota          Quota
	CPUUsed        int64
	MemoryUsed     int64
	StorageUsed    int64
	NetworkUsed    int64
	ParallelUsed   int
	Priority       int
	FairnessWeight float64
}

// GetTenantInfo returns comprehensive information about a tenant.
func (fs *FairScheduler) GetTenantInfo(tenant TenantID) TenantInfo {
	quota := fs.quotas.GetQuota(tenant)
	stats := fs.getOrCreateStats(tenant)

	return TenantInfo{
		Tenant:         tenant,
		Quota:          quota,
		CPUUsed:        stats.CPUUsed(),
		MemoryUsed:     stats.MemoryUsed(),
		StorageUsed:    stats.StorageUsed(),
		NetworkUsed:    stats.NetworkUsed(),
		ParallelUsed:   stats.ParallelUsed(),
		Priority:       quota.Priority,
		FairnessWeight: quota.FairnessWeight,
	}
}

// GetAllTenantsInfo returns information about all tenants.
func (fs *FairScheduler) GetAllTenantsInfo() []TenantInfo {
	fs.mu.RLock()
	tenants := make([]TenantID, 0, len(fs.stats))
	for tenant := range fs.stats {
		tenants = append(tenants, tenant)
	}
	fs.mu.RUnlock()
	infos := make([]TenantInfo, 0, len(tenants))
	for _, tenant := range tenants {
		infos = append(infos, fs.GetTenantInfo(tenant))
	}
	return infos
}

// ContextKey is the context key for tenant information.
type contextKey string

const tenantKey contextKey = "tenant"

// WithTenant adds tenant information to a context.
func WithTenant(ctx context.Context, tenant TenantID) context.Context {
	return context.WithValue(ctx, tenantKey, tenant)
}

// FromContext retrieves the tenant from a context.
func FromContext(ctx context.Context) (TenantID, bool) {
	tenant, ok := ctx.Value(tenantKey).(TenantID)
	return tenant, ok
}

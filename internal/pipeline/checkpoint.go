// Package pipeline provides checkpoint and resume functionality for pipeline execution.
// This enables resuming builds after worker loss or crashes.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	api "github.com/CYPT71/secure-oci-base/internal/core"
)

// Checkpoint represents a persistent checkpoint of pipeline execution state.
type Checkpoint struct {
	// ID is a unique identifier for this checkpoint
	ID string `json:"id"`
	// PipelineID is the ID of the pipeline this checkpoint belongs to
	PipelineID string `json:"pipeline_id"`
	// StageID is the ID of the stage this checkpoint represents
	StageID string `json:"stage_id"`
	// State is the current state of the stage
	State StageState `json:"state"`
	// Inputs is the hash of all inputs consumed by this stage
	Inputs string `json:"inputs"`
	// Outputs is the hash of all outputs produced by this stage
	Outputs string `json:"outputs"`
	// StartTime is when the stage started
	StartTime time.Time `json:"start_time"`
	// EndTime is when the stage completed (or last checkpoint)
	EndTime time.Time `json:"end_time"`
	// AttemptCount is the number of times this stage has been attempted
	AttemptCount int `json:"attempt_count"`
	// Retryable indicates if this stage can be retried
	Retryable bool `json:"retryable"`
	// Error contains the error message if the stage failed
	Error string `json:"error,omitempty"`
	// Metadata contains additional checkpoint metadata
	Metadata map[string]string `json:"metadata,omitempty"`
}

// CheckpointStoreInterface defines the interface for checkpoint storage.
type CheckpointStoreInterface interface {
	Save(cp *Checkpoint) error
	Get(id string) (*Checkpoint, bool)
	GetByStage(pipelineID, stageID string) (*Checkpoint, bool)
	Delete(id string) error
	DeleteByPipeline(pipelineID string) error
	ListByPipeline(pipelineID string) []*Checkpoint
	ListIncomplete() []*Checkpoint
	Import(r io.Reader) (*Checkpoint, error)
	Export(w io.Writer, id string) error
}

// CheckpointStore manages persistent checkpoints for pipeline execution.
type CheckpointStore struct {
	mu          sync.RWMutex
	checkpoints map[string]*Checkpoint
	basePath    string
}

// NewCheckpointStore creates a new CheckpointStore.
func NewCheckpointStore(basePath string) (*CheckpointStore, error) {
	if basePath == "" {
		return nil, errors.New("base path cannot be empty")
	}

	// Ensure base path exists
	if err := os.MkdirAll(basePath, 0700); err != nil {
		return nil, fmt.Errorf("failed to create checkpoint directory: %w", err)
	}

	store := &CheckpointStore{
		checkpoints: make(map[string]*Checkpoint),
		basePath:    basePath,
	}

	// Load existing checkpoints
	if err := store.load(); err != nil {
		return nil, fmt.Errorf("failed to load checkpoints: %w", err)
	}

	return store, nil
}

// load loads all checkpoints from the base path.
func (cs *CheckpointStore) load() error {
	entries, err := os.ReadDir(cs.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(cs.basePath, entry.Name()))
		if err != nil {
			continue // Skip files we can't read
		}

		var cp Checkpoint
		if err := json.Unmarshal(data, &cp); err != nil {
			continue // Skip invalid files
		}

		cs.checkpoints[cp.ID] = &cp
	}

	return nil
}

// Save saves a checkpoint to persistent storage.
func (cs *CheckpointStore) Save(cp *Checkpoint) error {
	if cp == nil {
		return errors.New("checkpoint cannot be nil")
	}
	if cp.ID == "" {
		return errors.New("checkpoint ID cannot be empty")
	}

	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal checkpoint: %w", err)
	}

	// Write atomically
	filename := filepath.Join(cs.basePath, cp.ID+".json")
	tmpFile := filename + ".tmp"

	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write checkpoint: %w", err)
	}

	if err := os.Rename(tmpFile, filename); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("failed to rename checkpoint: %w", err)
	}

	cs.mu.Lock()
	cs.checkpoints[cp.ID] = cp
	cs.mu.Unlock()

	return nil
}

// Get retrieves a checkpoint by ID.
func (cs *CheckpointStore) Get(id string) (*Checkpoint, bool) {
	cs.mu.RLock()
	cp, ok := cs.checkpoints[id]
	cs.mu.RUnlock()
	return cp, ok
}

// GetByStage retrieves the latest checkpoint for a specific stage.
func (cs *CheckpointStore) GetByStage(pipelineID, stageID string) (*Checkpoint, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	var latest *Checkpoint
	for _, cp := range cs.checkpoints {
		if cp.PipelineID == pipelineID && cp.StageID == stageID {
			if latest == nil || cp.EndTime.After(latest.EndTime) {
				latest = cp
			}
		}
	}

	if latest == nil {
		return nil, false
	}
	return latest, true
}

// Delete removes a checkpoint from the store.
func (cs *CheckpointStore) Delete(id string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	delete(cs.checkpoints, id)

	filename := filepath.Join(cs.basePath, id+".json")
	if err := os.Remove(filename); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete checkpoint: %w", err)
	}

	return nil
}

// DeleteByPipeline removes all checkpoints for a specific pipeline.
func (cs *CheckpointStore) DeleteByPipeline(pipelineID string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	for id, cp := range cs.checkpoints {
		if cp.PipelineID == pipelineID {
			delete(cs.checkpoints, id)
			filename := filepath.Join(cs.basePath, id+".json")
			os.Remove(filename)
		}
	}

	return nil
}

// ListByPipeline returns all checkpoints for a specific pipeline.
func (cs *CheckpointStore) ListByPipeline(pipelineID string) []*Checkpoint {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	var result []*Checkpoint
	for _, cp := range cs.checkpoints {
		if cp.PipelineID == pipelineID {
			result = append(result, cp)
		}
	}
	return result
}

// ListIncomplete returns all checkpoints that are not in a terminal state.
func (cs *CheckpointStore) ListIncomplete() []*Checkpoint {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	var result []*Checkpoint
	for _, cp := range cs.checkpoints {
		if cp.State != StageSucceeded && cp.State != StageFailed {
			result = append(result, cp)
		}
	}
	return result
}

// GenerateCheckpointID generates a unique checkpoint ID.
func GenerateCheckpointID(pipelineID, stageID string) string {
	hash := sha256.New()
	hash.Write([]byte(pipelineID + "|" + stageID + "|" + time.Now().Format(time.RFC3339Nano)))
	return hex.EncodeToString(hash.Sum(nil))[:16]
}

// CreateCheckpoint creates a new checkpoint for a stage.
func CreateCheckpoint(pipelineID, stageID string, state StageState) *Checkpoint {
	return &Checkpoint{
		ID:           GenerateCheckpointID(pipelineID, stageID),
		PipelineID:   pipelineID,
		StageID:      stageID,
		State:        state,
		StartTime:    time.Now(),
		EndTime:      time.Time{},
		AttemptCount: 1,
		Retryable:    true,
		Metadata:     make(map[string]string),
	}
}

// CheckpointManager manages checkpoints for pipeline execution.
type CheckpointManager struct {
	store       CheckpointStoreInterface
	pipelineID  string
	checkpoints map[string]*Checkpoint
	mu          sync.RWMutex
}

// NewCheckpointManager creates a new CheckpointManager for a specific pipeline.
func NewCheckpointManager(store CheckpointStoreInterface, pipelineID string) *CheckpointManager {
	return &CheckpointManager{
		store:       store,
		pipelineID:  pipelineID,
		checkpoints: make(map[string]*Checkpoint),
	}
}

// Create creates a new checkpoint for a stage.
func (cm *CheckpointManager) Create(stageID string, state StageState) *Checkpoint {
	cp := CreateCheckpoint(cm.pipelineID, stageID, state)

	cm.mu.Lock()
	cm.checkpoints[stageID] = cp
	cm.mu.Unlock()

	// Save asynchronously to avoid blocking
	go func() {
		cm.store.Save(cp)
	}()

	return cp
}

// Update updates an existing checkpoint.
func (cm *CheckpointManager) Update(stageID string, state StageState, outputs string, err error) *Checkpoint {
	cm.mu.RLock()
	cp, ok := cm.checkpoints[stageID]
	cm.mu.RUnlock()

	if !ok {
		// Create new checkpoint if it doesn't exist
		if err != nil {
			cp = CreateCheckpoint(cm.pipelineID, stageID, StageFailed)
			cp.Error = err.Error()
		} else {
			cp = CreateCheckpoint(cm.pipelineID, stageID, state)
		}
		cp.AttemptCount = 1
	} else {
		// Update existing checkpoint
		cp.State = state
		cp.EndTime = time.Now()
		cp.AttemptCount++
		if err != nil {
			cp.Error = err.Error()
		}
		if outputs != "" {
			cp.Outputs = outputs
		}
	}

	cm.mu.Lock()
	cm.checkpoints[stageID] = cp
	cm.mu.Unlock()

	// Save asynchronously
	go func() {
		cm.store.Save(cp)
	}()

	return cp
}

// Get retrieves a checkpoint by stage ID.
func (cm *CheckpointManager) Get(stageID string) (*Checkpoint, bool) {
	cm.mu.RLock()
	cp, ok := cm.checkpoints[stageID]
	cm.mu.RUnlock()
	return cp, ok
}

// CanResume checks if a specific stage can be resumed from a checkpoint.
func (cm *CheckpointManager) CanResume(stageID string) bool {
	cp, ok := cm.Get(stageID)
	if !ok {
		return false
	}
	return cp.Retryable && (cp.State == StageCanceled || cp.State == StageBudgetExceeded)
}

// GetResumePoint returns the stage ID from which to resume execution.
// Returns empty string if no resume is possible.
func (cm *CheckpointManager) GetResumePoint() string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	// Find the first incomplete stage in topological order
	// In a real implementation, this would query the pipeline DAG
	for _, cp := range cm.checkpoints {
		if cp.State != StageSucceeded && cp.State != StageFailed {
			return cp.StageID
		}
	}

	return ""
}

// Delete deletes all checkpoints for this pipeline.
func (cm *CheckpointManager) Delete() error {
	return cm.store.DeleteByPipeline(cm.pipelineID)
}

// ResumeInfo contains information needed to resume pipeline execution.
type ResumeInfo struct {
	// PipelineID is the ID of the pipeline to resume
	PipelineID string
	// StartStage is the stage to start from
	StartStage string
	// CompletedStages is the list of stages already completed
	CompletedStages []string
	// Checkpoints contains all checkpoints for the pipeline
	Checkpoints []*Checkpoint
}

// GetResumeInfo returns information needed to resume pipeline execution.
func (cm *CheckpointManager) GetResumeInfo() ResumeInfo {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	info := ResumeInfo{
		PipelineID:      cm.pipelineID,
		Checkpoints:     make([]*Checkpoint, 0, len(cm.checkpoints)),
		CompletedStages: make([]string, 0),
	}

	// Find all incomplete stages first
	var incompleteStages []string
	for stageID, cp := range cm.checkpoints {
		info.Checkpoints = append(info.Checkpoints, cp)
		if cp.State == StageSucceeded {
			info.CompletedStages = append(info.CompletedStages, stageID)
		}
		if cp.State != StageSucceeded && cp.State != StageFailed {
			incompleteStages = append(incompleteStages, stageID)
		}
	}

	// Sort incomplete stages by name for deterministic behavior
	// This ensures we always pick the same stage regardless of map iteration order
	sort.Strings(incompleteStages)

	// Start from the first incomplete stage (alphabetically first)
	if len(incompleteStages) > 0 {
		info.StartStage = incompleteStages[0]
	}

	return info
}

// CheckpointableRunner wraps a StageRunner to add checkpoint functionality.
type CheckpointableRunner struct {
	Runner     StageRunner
	Manager    *CheckpointManager
	OutputHash string
}

// Run runs a stage with checkpoint support.
func (cr *CheckpointableRunner) Run(ctx context.Context, stage api.Stage) error {
	// Check if we can resume from a checkpoint first
	if resumeCp, ok := cr.Manager.Get(stage.ID); ok {
		if resumeCp.State == StageSucceeded {
			// Stage already completed successfully
			return nil
		}
		if resumeCp.State == StageFailed && !resumeCp.Retryable {
			// Stage failed and is not retryable
			return fmt.Errorf("stage %s failed and is not retryable: %s", stage.ID, resumeCp.Error)
		}
	}

	// Create new checkpoint
	cp := cr.Manager.Create(stage.ID, StageCanceled)

	// If we have a resumable checkpoint, update attempt count
	if resumeCp, ok := cr.Manager.Get(stage.ID); ok {
		cp.AttemptCount = resumeCp.AttemptCount + 1
	}

	// Run the stage
	err := cr.Runner.Run(ctx, stage)
	if err != nil {
		cp.State = StageFailed
		cp.Error = err.Error()
		cr.Manager.Update(stage.ID, cp.State, cp.Outputs, err)
		return err
	}

	cp.State = StageSucceeded
	cp.Outputs = cr.OutputHash
	cr.Manager.Update(stage.ID, cp.State, cp.Outputs, nil)
	return nil
}

// PipelineCheckpointAdapter adds checkpoint support to a Scheduler.
type PipelineCheckpointAdapter struct {
	Scheduler   Scheduler
	Checkpoints *CheckpointManager
}

// Run runs the pipeline with checkpoint support.
func (pca *PipelineCheckpointAdapter) Run(ctx context.Context, definition api.Pipeline) (ScheduleResult, error) {
	// Check if we can resume from a checkpoint
	resumeInfo := pca.Checkpoints.GetResumeInfo()

	if resumeInfo.StartStage != "" {
		// Resume from checkpoint
		return pca.resumeFromCheckpoint(ctx, definition, resumeInfo)
	}

	// Run normally
	return pca.Scheduler.Run(ctx, definition)
}

// resumeFromCheckpoint resumes pipeline execution from a checkpoint.
func (pca *PipelineCheckpointAdapter) resumeFromCheckpoint(ctx context.Context, definition api.Pipeline, resumeInfo ResumeInfo) (ScheduleResult, error) {
	// Create a new context that includes resume information
	// In a real implementation, this would modify the pipeline definition
	// to skip already-completed stages

	// For now, just run the full pipeline
	// The CheckpointableRunner will handle skipping completed stages
	return pca.Scheduler.Run(ctx, definition)
}

// WithCheckpoints wraps a Scheduler to add checkpoint support.
func WithCheckpoints(scheduler Scheduler, manager *CheckpointManager) *PipelineCheckpointAdapter {
	return &PipelineCheckpointAdapter{
		Scheduler:   scheduler,
		Checkpoints: manager,
	}
}

// MemoryCheckpointStore is an in-memory checkpoint store for testing.
type MemoryCheckpointStore struct {
	mu          sync.RWMutex
	checkpoints map[string]*Checkpoint
}

// NewMemoryCheckpointStore creates a new in-memory checkpoint store.
func NewMemoryCheckpointStore() *MemoryCheckpointStore {
	return &MemoryCheckpointStore{
		checkpoints: make(map[string]*Checkpoint),
	}
}

// Save saves a checkpoint to memory.
func (mcs *MemoryCheckpointStore) Save(cp *Checkpoint) error {
	if cp == nil {
		return errors.New("checkpoint cannot be nil")
	}
	mcs.mu.Lock()
	mcs.checkpoints[cp.ID] = cp
	mcs.mu.Unlock()
	return nil
}

// Get retrieves a checkpoint by ID.
func (mcs *MemoryCheckpointStore) Get(id string) (*Checkpoint, bool) {
	mcs.mu.RLock()
	cp, ok := mcs.checkpoints[id]
	mcs.mu.RUnlock()
	return cp, ok
}

// GetByStage retrieves the latest checkpoint for a specific stage.
func (mcs *MemoryCheckpointStore) GetByStage(pipelineID, stageID string) (*Checkpoint, bool) {
	mcs.mu.RLock()
	defer mcs.mu.RUnlock()

	var latest *Checkpoint
	for _, cp := range mcs.checkpoints {
		if cp.PipelineID == pipelineID && cp.StageID == stageID {
			if latest == nil || cp.EndTime.After(latest.EndTime) {
				latest = cp
			}
		}
	}

	if latest == nil {
		return nil, false
	}
	return latest, true
}

// Delete removes a checkpoint from the store.
func (mcs *MemoryCheckpointStore) Delete(id string) error {
	mcs.mu.Lock()
	defer mcs.mu.Unlock()
	delete(mcs.checkpoints, id)
	return nil
}

// DeleteByPipeline removes all checkpoints for a specific pipeline.
func (mcs *MemoryCheckpointStore) DeleteByPipeline(pipelineID string) error {
	mcs.mu.Lock()
	defer mcs.mu.Unlock()

	for id, cp := range mcs.checkpoints {
		if cp.PipelineID == pipelineID {
			delete(mcs.checkpoints, id)
		}
	}
	return nil
}

// ListByPipeline returns all checkpoints for a specific pipeline.
func (mcs *MemoryCheckpointStore) ListByPipeline(pipelineID string) []*Checkpoint {
	mcs.mu.RLock()
	defer mcs.mu.RUnlock()

	var result []*Checkpoint
	for _, cp := range mcs.checkpoints {
		if cp.PipelineID == pipelineID {
			result = append(result, cp)
		}
	}
	return result
}

// ListIncomplete returns all checkpoints that are not in a terminal state.
func (mcs *MemoryCheckpointStore) ListIncomplete() []*Checkpoint {
	mcs.mu.RLock()
	defer mcs.mu.RUnlock()

	var result []*Checkpoint
	for _, cp := range mcs.checkpoints {
		if cp.State != StageSucceeded && cp.State != StageFailed {
			result = append(result, cp)
		}
	}
	return result
}

// Import imports a checkpoint from a reader.
func (mcs *MemoryCheckpointStore) Import(r io.Reader) (*Checkpoint, error) {
	var cp Checkpoint
	if err := json.NewDecoder(r).Decode(&cp); err != nil {
		return nil, err
	}
	if err := mcs.Save(&cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

// Export exports a checkpoint to a writer.
func (mcs *MemoryCheckpointStore) Export(w io.Writer, id string) error {
	cp, ok := mcs.Get(id)
	if !ok {
		return fmt.Errorf("checkpoint %s not found", id)
	}
	return json.NewEncoder(w).Encode(cp)
}

// FileCheckpointStore is a file-based checkpoint store.
type FileCheckpointStore = CheckpointStore

// DefaultCheckpointPath is the default path for storing checkpoints.
const DefaultCheckpointPath = "/var/lib/platform-factory/checkpoints"

// OpenCheckpointStore opens the checkpoint store at the default path.
func OpenCheckpointStore() (*CheckpointStore, error) {
	return NewCheckpointStore(DefaultCheckpointPath)
}

// OpenCheckpointStoreAt opens the checkpoint store at a specific path.
func OpenCheckpointStoreAt(path string) (*CheckpointStore, error) {
	return NewCheckpointStore(path)
}

// Import imports a checkpoint from a reader.
func (cs *CheckpointStore) Import(r io.Reader) (*Checkpoint, error) {
	var cp Checkpoint
	if err := json.NewDecoder(r).Decode(&cp); err != nil {
		return nil, err
	}

	if err := cs.Save(&cp); err != nil {
		return nil, err
	}

	return &cp, nil
}

// Export exports a checkpoint to a writer.
func (cs *CheckpointStore) Export(w io.Writer, id string) error {
	cp, ok := cs.Get(id)
	if !ok {
		return fmt.Errorf("checkpoint %s not found", id)
	}

	return json.NewEncoder(w).Encode(cp)
}

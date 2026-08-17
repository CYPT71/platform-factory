package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	api "github.com/CYPT71/platform-factory/internal/core"
)

func TestCheckpointIDGeneration(t *testing.T) {
	id1 := GenerateCheckpointID("pipeline-1", "stage-1")
	id2 := GenerateCheckpointID("pipeline-1", "stage-1")
	id3 := GenerateCheckpointID("pipeline-2", "stage-1")

	// Same inputs should produce different IDs due to timestamp
	if id1 == id2 {
		t.Error("expected different checkpoint IDs for same inputs at different times")
	}

	// Different pipeline should produce different ID
	if id1 == id3 {
		t.Error("expected different checkpoint IDs for different pipelines")
	}

	// IDs should be 16 characters (128 bits in hex)
	if len(id1) != 16 {
		t.Errorf("expected ID length 16, got %d", len(id1))
	}
}

func TestCreateCheckpoint(t *testing.T) {
	cp := CreateCheckpoint("pipeline-1", "stage-1", StageCanceled)

	if cp.ID == "" {
		t.Error("expected non-empty ID")
	}
	if cp.PipelineID != "pipeline-1" {
		t.Errorf("expected PipelineID pipeline-1, got %s", cp.PipelineID)
	}
	if cp.StageID != "stage-1" {
		t.Errorf("expected StageID stage-1, got %s", cp.StageID)
	}
	if cp.State != StageCanceled {
		t.Errorf("expected State %s, got %s", StageCanceled, cp.State)
	}
	if cp.AttemptCount != 1 {
		t.Errorf("expected AttemptCount 1, got %d", cp.AttemptCount)
	}
	if !cp.Retryable {
		t.Error("expected Retryable to be true")
	}
	if cp.Metadata == nil {
		t.Error("expected Metadata to be initialized")
	}
	if cp.StartTime.IsZero() {
		t.Error("expected StartTime to be set")
	}
	// EndTime is zero for new checkpoints and set when state transitions
	if !cp.EndTime.IsZero() {
		t.Error("expected EndTime to be zero for new checkpoint")
	}
}

func TestMemoryCheckpointStore(t *testing.T) {
	store := NewMemoryCheckpointStore()

	// Test Save and Get
	cp := CreateCheckpoint("pipeline-1", "stage-1", StageCanceled)
	cp.ID = "test-id-1"

	err := store.Save(cp)
	if err != nil {
		t.Fatalf("failed to save checkpoint: %v", err)
	}

	got, ok := store.Get("test-id-1")
	if !ok {
		t.Fatal("expected to find checkpoint")
	}
	if got.ID != cp.ID {
		t.Errorf("expected ID %s, got %s", cp.ID, got.ID)
	}

	// Test GetByStage
	got2, ok := store.GetByStage("pipeline-1", "stage-1")
	if !ok {
		t.Fatal("expected to find checkpoint by stage")
	}
	if got2.ID != cp.ID {
		t.Errorf("expected ID %s, got %s", cp.ID, got2.ID)
	}

	// Test Delete
	err = store.Delete("test-id-1")
	if err != nil {
		t.Fatalf("failed to delete checkpoint: %v", err)
	}

	_, ok = store.Get("test-id-1")
	if ok {
		t.Error("expected checkpoint to be deleted")
	}
}

func TestMemoryCheckpointStoreList(t *testing.T) {
	store := NewMemoryCheckpointStore()

	// Add multiple checkpoints
	for i := 0; i < 5; i++ {
		cp := CreateCheckpoint("pipeline-1", "stage-1", StageCanceled)
		cp.ID = "test-id-" + string(rune('a'+i))
		cp.StageID = "stage-" + string(rune('a'+i))
		store.Save(cp)
	}

	// Add checkpoints for different pipeline
	for i := 0; i < 3; i++ {
		cp := CreateCheckpoint("pipeline-2", "stage-1", StageCanceled)
		cp.ID = "test-id-2-" + string(rune('a'+i))
		cp.StageID = "stage-" + string(rune('a'+i))
		store.Save(cp)
	}

	// Test ListByPipeline
	list1 := store.ListByPipeline("pipeline-1")
	if len(list1) != 5 {
		t.Errorf("expected 5 checkpoints for pipeline-1, got %d", len(list1))
	}

	list2 := store.ListByPipeline("pipeline-2")
	if len(list2) != 3 {
		t.Errorf("expected 3 checkpoints for pipeline-2, got %d", len(list2))
	}

	// Test ListIncomplete
	// Add a succeeded checkpoint
	succeeded := CreateCheckpoint("pipeline-1", "stage-succeeded", StageSucceeded)
	succeeded.ID = "succeeded-id"
	store.Save(succeeded)

	// Add a failed checkpoint
	failed := CreateCheckpoint("pipeline-1", "stage-failed", StageFailed)
	failed.ID = "failed-id"
	store.Save(failed)

	incomplete := store.ListIncomplete()
	// Should return all except succeeded and failed
	// pipeline-1 has 5 + 1 succeeded + 1 failed = 7
	// pipeline-2 has 3
	// Total = 10, minus 2 terminal (succeeded, failed) = 8 incomplete
	if len(incomplete) != 8 {
		t.Errorf("expected 8 incomplete checkpoints, got %d", len(incomplete))
	}
}

func TestMemoryCheckpointStoreDeleteByPipeline(t *testing.T) {
	store := NewMemoryCheckpointStore()

	// Add checkpoints for two pipelines
	for i := 0; i < 3; i++ {
		cp := CreateCheckpoint("pipeline-1", "stage-1", StageCanceled)
		cp.ID = "id-1-" + string(rune('a'+i))
		store.Save(cp)
	}
	for i := 0; i < 3; i++ {
		cp := CreateCheckpoint("pipeline-2", "stage-1", StageCanceled)
		cp.ID = "id-2-" + string(rune('a'+i))
		store.Save(cp)
	}

	// Delete pipeline-1 checkpoints
	err := store.DeleteByPipeline("pipeline-1")
	if err != nil {
		t.Fatalf("failed to delete by pipeline: %v", err)
	}

	// Verify pipeline-1 checkpoints are deleted
	list1 := store.ListByPipeline("pipeline-1")
	if len(list1) != 0 {
		t.Errorf("expected 0 checkpoints for pipeline-1, got %d", len(list1))
	}

	// Verify pipeline-2 checkpoints still exist
	list2 := store.ListByPipeline("pipeline-2")
	if len(list2) != 3 {
		t.Errorf("expected 3 checkpoints for pipeline-2, got %d", len(list2))
	}
}

func TestFileCheckpointStore(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "checkpoint-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create checkpoint store: %v", err)
	}

	// Test Save and Get
	cp := CreateCheckpoint("pipeline-1", "stage-1", StageCanceled)
	cp.ID = "file-test-id"

	err = store.Save(cp)
	if err != nil {
		t.Fatalf("failed to save checkpoint: %v", err)
	}

	got, ok := store.Get("file-test-id")
	if !ok {
		t.Fatal("expected to find checkpoint")
	}
	if got.ID != cp.ID {
		t.Errorf("expected ID %s, got %s", cp.ID, got.ID)
	}

	// Verify file exists
	filename := filepath.Join(tmpDir, "file-test-id.json")
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		t.Error("expected checkpoint file to exist")
	}

	// Test persistence by creating new store
	store2, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create new checkpoint store: %v", err)
	}

	got2, ok := store2.Get("file-test-id")
	if !ok {
		t.Fatal("expected to find checkpoint in new store")
	}
	if got2.ID != cp.ID {
		t.Errorf("expected ID %s, got %s", cp.ID, got2.ID)
	}

	// Test Delete
	err = store.Delete("file-test-id")
	if err != nil {
		t.Fatalf("failed to delete checkpoint: %v", err)
	}

	_, ok = store.Get("file-test-id")
	if ok {
		t.Error("expected checkpoint to be deleted")
	}

	// Verify file is deleted
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		t.Error("expected checkpoint file to be deleted")
	}
}

func TestFileCheckpointStoreLoadCorrupted(t *testing.T) {
	// Create temp directory with corrupted file
	tmpDir, err := os.MkdirTemp("", "checkpoint-corrupted-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a corrupted JSON file
	corruptedData := []byte("{invalid json")
	corruptedFile := filepath.Join(tmpDir, "corrupted.json")
	if err := os.WriteFile(corruptedFile, corruptedData, 0600); err != nil {
		t.Fatalf("failed to create corrupted file: %v", err)
	}

	// Create a valid checkpoint file
	cp := CreateCheckpoint("pipeline-1", "stage-1", StageCanceled)
	cp.ID = "valid-id"
	validData, _ := json.Marshal(cp)
	validFile := filepath.Join(tmpDir, "valid-id.json")
	if err := os.WriteFile(validFile, validData, 0600); err != nil {
		t.Fatalf("failed to create valid file: %v", err)
	}

	// Load store - should skip corrupted file
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create checkpoint store: %v", err)
	}

	// Valid checkpoint should be loaded
	_, ok := store.Get("valid-id")
	if !ok {
		t.Error("expected valid checkpoint to be loaded")
	}

	// Corrupted checkpoint should not be loaded
	_, ok = store.Get("corrupted")
	if ok {
		t.Error("expected corrupted checkpoint to not be loaded")
	}
}

func TestCheckpointManager(t *testing.T) {
	store := NewMemoryCheckpointStore()
	manager := NewCheckpointManager(store, "pipeline-1")

	// Test Create
	cp := manager.Create("stage-1", StageCanceled)
	if cp == nil {
		t.Fatal("expected non-nil checkpoint")
	}
	if cp.PipelineID != "pipeline-1" {
		t.Errorf("expected PipelineID pipeline-1, got %s", cp.PipelineID)
	}
	if cp.StageID != "stage-1" {
		t.Errorf("expected StageID stage-1, got %s", cp.StageID)
	}

	// Test Get
	got, ok := manager.Get("stage-1")
	if !ok {
		t.Fatal("expected to find checkpoint")
	}
	if got.ID != cp.ID {
		t.Errorf("expected ID %s, got %s", cp.ID, got.ID)
	}

	// Test Update
	updated := manager.Update("stage-1", StageSucceeded, "output-hash", nil)
	if updated.State != StageSucceeded {
		t.Errorf("expected State %s, got %s", StageSucceeded, updated.State)
	}
	if updated.Outputs != "output-hash" {
		t.Errorf("expected Outputs output-hash, got %s", updated.Outputs)
	}
	if updated.AttemptCount != 2 {
		t.Errorf("expected AttemptCount 2, got %d", updated.AttemptCount)
	}

	// Test Update with error
	updated = manager.Update("stage-1", StageFailed, "", fmt.Errorf("test error"))
	if updated.State != StageFailed {
		t.Errorf("expected State %s, got %s", StageFailed, updated.State)
	}
	if updated.Error != "test error" {
		t.Errorf("expected Error test error, got %s", updated.Error)
	}

	// Test CanResume
	canResume := manager.CanResume("stage-1")
	if canResume {
		t.Error("expected stage-1 to not be resumable (failed, not retryable)")
	}

	// Create a resumable checkpoint
	resumable := CreateCheckpoint("pipeline-1", "stage-2", StageCanceled)
	resumable.Retryable = true
	store.Save(resumable)
	manager.checkpoints["stage-2"] = resumable

	canResume = manager.CanResume("stage-2")
	if !canResume {
		t.Error("expected stage-2 to be resumable")
	}

	// Test GetResumePoint
	resumePoint := manager.GetResumePoint()
	if resumePoint != "stage-2" {
		t.Errorf("expected resume point stage-2, got %s", resumePoint)
	}
}

func TestCheckpointManagerGetResumeInfo(t *testing.T) {
	store := NewMemoryCheckpointStore()
	manager := NewCheckpointManager(store, "pipeline-1")

	// Create checkpoints in different states
	cp1 := CreateCheckpoint("pipeline-1", "stage-1", StageSucceeded)
	cp1.Retryable = true
	cp1.AttemptCount = 1
	store.Save(cp1)
	manager.checkpoints["stage-1"] = cp1

	cp2 := CreateCheckpoint("pipeline-1", "stage-2", StageCanceled)
	cp2.Retryable = true
	cp2.AttemptCount = 2
	store.Save(cp2)
	manager.checkpoints["stage-2"] = cp2

	cp3 := CreateCheckpoint("pipeline-1", "stage-3", StageCanceled)
	cp3.Retryable = true
	cp3.AttemptCount = 1
	store.Save(cp3)
	manager.checkpoints["stage-3"] = cp3

	info := manager.GetResumeInfo()

	if info.PipelineID != "pipeline-1" {
		t.Errorf("expected PipelineID pipeline-1, got %s", info.PipelineID)
	}
	if info.StartStage != "stage-2" {
		t.Errorf("expected StartStage stage-2, got %s", info.StartStage)
	}
	if len(info.CompletedStages) != 1 {
		t.Errorf("expected 1 completed stage, got %d", len(info.CompletedStages))
	}
	if info.CompletedStages[0] != "stage-1" {
		t.Errorf("expected completed stage stage-1, got %s", info.CompletedStages[0])
	}
	if len(info.Checkpoints) != 3 {
		t.Errorf("expected 3 checkpoints, got %d", len(info.Checkpoints))
	}
}

func TestCheckpointSerialization(t *testing.T) {
	cp := CreateCheckpoint("pipeline-1", "stage-1", StageCanceled)
	cp.ID = "test-id"
	cp.AttemptCount = 3
	cp.Retryable = false
	cp.Error = "test error"
	cp.Metadata = map[string]string{"key": "value"}

	data, err := json.Marshal(cp)
	if err != nil {
		t.Fatalf("failed to marshal checkpoint: %v", err)
	}

	var unmarshaled Checkpoint
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("failed to unmarshal checkpoint: %v", err)
	}

	if unmarshaled.ID != cp.ID {
		t.Errorf("expected ID %s, got %s", cp.ID, unmarshaled.ID)
	}
	if unmarshaled.PipelineID != cp.PipelineID {
		t.Errorf("expected PipelineID %s, got %s", cp.PipelineID, unmarshaled.PipelineID)
	}
	if unmarshaled.StageID != cp.StageID {
		t.Errorf("expected StageID %s, got %s", cp.StageID, unmarshaled.StageID)
	}
	if unmarshaled.State != cp.State {
		t.Errorf("expected State %s, got %s", cp.State, unmarshaled.State)
	}
	if unmarshaled.AttemptCount != cp.AttemptCount {
		t.Errorf("expected AttemptCount %d, got %d", cp.AttemptCount, unmarshaled.AttemptCount)
	}
	if unmarshaled.Retryable != cp.Retryable {
		t.Errorf("expected Retryable %v, got %v", cp.Retryable, unmarshaled.Retryable)
	}
	if unmarshaled.Error != cp.Error {
		t.Errorf("expected Error %s, got %s", cp.Error, unmarshaled.Error)
	}
	if unmarshaled.Metadata["key"] != "value" {
		t.Errorf("expected Metadata[key] value, got %s", unmarshaled.Metadata["key"])
	}
}

func TestCheckpointStoreImportExport(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "checkpoint-import-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create checkpoint store: %v", err)
	}

	// Create a checkpoint
	cp := CreateCheckpoint("pipeline-1", "stage-1", StageCanceled)
	cp.ID = "import-test-id"

	// Save checkpoint first
	err = store.Save(cp)
	if err != nil {
		t.Fatalf("failed to save checkpoint: %v", err)
	}

	// Export to buffer
	var buf bytes.Buffer
	err = store.Export(&buf, cp.ID)
	if err != nil {
		t.Fatalf("failed to export checkpoint: %v", err)
	}

	// Import from buffer
	imported, err := store.Import(&buf)
	if err != nil {
		t.Fatalf("failed to import checkpoint: %v", err)
	}

	if imported.ID != cp.ID {
		t.Errorf("expected ID %s, got %s", cp.ID, imported.ID)
	}
	if imported.PipelineID != cp.PipelineID {
		t.Errorf("expected PipelineID %s, got %s", cp.PipelineID, imported.PipelineID)
	}
}

func TestCheckpointableRunner(t *testing.T) {
	store := NewMemoryCheckpointStore()
	manager := NewCheckpointManager(store, "pipeline-1")

	// Create a mock stage runner
	mockRunner := &mockStageRunner{}

	runner := &CheckpointableRunner{
		Runner:  mockRunner,
		Manager: manager,
	}

	stage := api.Stage{
		ID: "stage-1",
	}

	// First run - should succeed
	err := runner.Run(context.Background(), stage)
	if err != nil {
		t.Fatalf("unexpected error on first run: %v", err)
	}

	// Check checkpoint was created
	cp, ok := manager.Get("stage-1")
	if !ok {
		t.Fatal("expected checkpoint to be created")
	}
	if cp.State != StageSucceeded {
		t.Errorf("expected State %s, got %s", StageSucceeded, cp.State)
	}

	// Second run - should use existing checkpoint
	mockRunner.fail = true
	err = runner.Run(context.Background(), stage)
	if err != nil {
		t.Errorf("unexpected error on second run: %v", err)
	}
}

func TestWithCheckpoints(t *testing.T) {
	store := NewMemoryCheckpointStore()
	manager := NewCheckpointManager(store, "pipeline-1")

	scheduler := Scheduler{
		Parallelism: 1,
		Runner: StageRunnerFunc(func(ctx context.Context, stage api.Stage) error {
			return nil
		}),
	}

	wrapped := WithCheckpoints(scheduler, manager)
	if wrapped == nil {
		t.Fatal("expected non-nil wrapped scheduler")
	}
}

func TestPipelineCheckpointAdapterRun(t *testing.T) {
	store := NewMemoryCheckpointStore()
	manager := NewCheckpointManager(store, "pipeline-1")

	scheduler := Scheduler{
		Parallelism: 1,
		Runner: StageRunnerFunc(func(ctx context.Context, stage api.Stage) error {
			return nil
		}),
	}

	adapter := WithCheckpoints(scheduler, manager)

	// Test normal run without checkpoints
	definition := api.Pipeline{
		APIVersion: "platform-factory.dev/v1alpha1",
		Name:       "test-pipeline",
		Stages: []api.Stage{
			{
				ID:      "stage-1",
				Command: api.Command{Executable: "/bin/true"},
			},
		},
	}

	result, err := adapter.Run(context.Background(), definition)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Find stage-1 in results
	found := false
	for _, stageResult := range result.Stages {
		if stageResult.Stage == "stage-1" {
			found = true
			if stageResult.State != StageSucceeded {
				t.Errorf("expected stage-1 to succeed, got %s", stageResult.State)
			}
			break
		}
	}
	if !found {
		t.Error("expected to find stage-1 in results")
	}

	// Test with existing checkpoint - should resume
	cp := CreateCheckpoint("pipeline-1", "stage-1", StageCanceled)
	cp.Retryable = true
	store.Save(cp)
	manager.checkpoints["stage-1"] = cp

	result, err = adapter.Run(context.Background(), definition)
	if err != nil {
		t.Fatalf("unexpected error on resume: %v", err)
	}
}

func TestPipelineCheckpointAdapterResumeFromCheckpoint(t *testing.T) {
	store := NewMemoryCheckpointStore()
	manager := NewCheckpointManager(store, "pipeline-1")

	// Create a checkpoint with completed stage
	cp := CreateCheckpoint("pipeline-1", "stage-1", StageSucceeded)
	cp.ID = "completed-checkpoint"
	cp.Retryable = false
	store.Save(cp)
	manager.checkpoints["stage-1"] = cp

	scheduler := Scheduler{
		Parallelism: 1,
		Runner: StageRunnerFunc(func(ctx context.Context, stage api.Stage) error {
			return nil
		}),
	}

	adapter := WithCheckpoints(scheduler, manager)

	definition := api.Pipeline{
		APIVersion: "platform-factory.dev/v1alpha1",
		Name:       "test-pipeline",
		Stages: []api.Stage{
			{ID: "stage-1", Command: api.Command{Executable: "/bin/true"}},
			{ID: "stage-2", Command: api.Command{Executable: "/bin/true"}},
		},
	}

	// Should handle checkpoint and run
	_, err := adapter.Run(context.Background(), definition)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// mockStageRunner is a mock implementation of StageRunner for testing
type mockStageRunner struct {
	fail      bool
	callCount int
}

func (m *mockStageRunner) Run(ctx context.Context, stage api.Stage) error {
	m.callCount++
	if m.fail {
		return fmt.Errorf("mock error")
	}
	return nil
}

func TestResumedCheckpoint(t *testing.T) {
	store := NewMemoryCheckpointStore()
	manager := NewCheckpointManager(store, "pipeline-1")

	// Create a checkpoint that can be resumed
	cp := CreateCheckpoint("pipeline-1", "stage-1", StageCanceled)
	cp.ID = "resume-test-id"
	cp.Retryable = true
	cp.AttemptCount = 1
	store.Save(cp)
	manager.checkpoints["stage-1"] = cp

	// Test CanResume
	if !manager.CanResume("stage-1") {
		t.Error("expected stage-1 to be resumable")
	}

	// Test Update to set as succeeded
	updated := manager.Update("stage-1", StageSucceeded, "output-hash", nil)
	if updated.State != StageSucceeded {
		t.Errorf("expected State %s, got %s", StageSucceeded, updated.State)
	}
	if updated.Outputs != "output-hash" {
		t.Errorf("expected Outputs output-hash, got %s", updated.Outputs)
	}
	if updated.AttemptCount != 2 {
		t.Errorf("expected AttemptCount 2, got %d", updated.AttemptCount)
	}

	// After succeeding, should not be resumable
	if manager.CanResume("stage-1") {
		t.Error("expected stage-1 to not be resumable after succeeding")
	}

	// Test GetResumePoint returns empty when all stages succeeded
	resumePoint := manager.GetResumePoint()
	if resumePoint != "" {
		t.Errorf("expected empty resume point, got %s", resumePoint)
	}
}

func TestCheckpointTimeout(t *testing.T) {
	store := NewMemoryCheckpointStore()
	manager := NewCheckpointManager(store, "pipeline-1")

	// Create a checkpoint
	cp := manager.Create("stage-1", StageCanceled)

	// Verify StartTime is set, EndTime should be zero for new checkpoint
	if cp.StartTime.IsZero() {
		t.Error("expected StartTime to be set")
	}
	if !cp.EndTime.IsZero() {
		t.Error("expected EndTime to be zero for new checkpoint")
	}

	// Wait a bit to ensure time difference
	time.Sleep(50 * time.Millisecond)

	// Update checkpoint
	updated := manager.Update("stage-1", StageSucceeded, "", nil)

	// EndTime should be set after update
	if updated.EndTime.IsZero() {
		t.Error("expected EndTime to be set after update")
	}
	// EndTime should be after StartTime
	if !updated.EndTime.After(cp.StartTime) {
		t.Error("expected EndTime to be after StartTime")
	}
}

func TestDefaultCheckpointPath(t *testing.T) {
	if DefaultCheckpointPath == "" {
		t.Error("expected DefaultCheckpointPath to be set")
	}
}

func TestFileCheckpointStoreOperations(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "file-store-ops-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// Create and save checkpoints
	cp1 := CreateCheckpoint("pipeline-1", "stage-1", StageCanceled)
	cp1.ID = "file-cp-1"
	err = store.Save(cp1)
	if err != nil {
		t.Fatalf("failed to save cp1: %v", err)
	}

	cp2 := CreateCheckpoint("pipeline-1", "stage-2", StageSucceeded)
	cp2.ID = "file-cp-2"
	err = store.Save(cp2)
	if err != nil {
		t.Fatalf("failed to save cp2: %v", err)
	}

	cp3 := CreateCheckpoint("pipeline-2", "stage-1", StageCanceled)
	cp3.ID = "file-cp-3"
	err = store.Save(cp3)
	if err != nil {
		t.Fatalf("failed to save cp3: %v", err)
	}

	// Test GetByStage
	got, ok := store.GetByStage("pipeline-1", "stage-1")
	if !ok {
		t.Error("expected to find checkpoint by stage")
	}
	if got.ID != cp1.ID {
		t.Errorf("expected ID %s, got %s", cp1.ID, got.ID)
	}

	// Test ListByPipeline
	list1 := store.ListByPipeline("pipeline-1")
	if len(list1) != 2 {
		t.Errorf("expected 2 checkpoints for pipeline-1, got %d", len(list1))
	}

	list2 := store.ListByPipeline("pipeline-2")
	if len(list2) != 1 {
		t.Errorf("expected 1 checkpoint for pipeline-2, got %d", len(list2))
	}

	// Test ListIncomplete
	incomplete := store.ListIncomplete()
	if len(incomplete) != 2 {
		t.Errorf("expected 2 incomplete checkpoints, got %d", len(incomplete))
	}

	// Test DeleteByPipeline
	err = store.DeleteByPipeline("pipeline-1")
	if err != nil {
		t.Fatalf("failed to delete by pipeline: %v", err)
	}

	// Verify deletion
	list1 = store.ListByPipeline("pipeline-1")
	if len(list1) != 0 {
		t.Errorf("expected 0 checkpoints for pipeline-1 after delete, got %d", len(list1))
	}

	list2 = store.ListByPipeline("pipeline-2")
	if len(list2) != 1 {
		t.Errorf("expected 1 checkpoint for pipeline-2 after delete, got %d", len(list2))
	}
}

func TestMemoryCheckpointStoreImportExport(t *testing.T) {
	store := NewMemoryCheckpointStore()

	// Create and save a checkpoint
	cp := CreateCheckpoint("pipeline-1", "stage-1", StageCanceled)
	cp.ID = "memory-import-test"
	cp.Outputs = "memory-test-hash"
	cp.AttemptCount = 3

	err := store.Save(cp)
	if err != nil {
		t.Fatalf("failed to save: %v", err)
	}

	// Export it
	var buf bytes.Buffer
	err = store.Export(&buf, cp.ID)
	if err != nil {
		t.Fatalf("failed to export: %v", err)
	}

	// Delete from store
	err = store.Delete(cp.ID)
	if err != nil {
		t.Fatalf("failed to delete: %v", err)
	}

	// Import it back
	imported, err := store.Import(&buf)
	if err != nil {
		t.Fatalf("failed to import: %v", err)
	}

	if imported.ID != cp.ID {
		t.Errorf("expected ID %s, got %s", cp.ID, imported.ID)
	}
	if imported.Outputs != cp.Outputs {
		t.Errorf("expected Outputs %s, got %s", cp.Outputs, imported.Outputs)
	}
	if imported.AttemptCount != cp.AttemptCount {
		t.Errorf("expected AttemptCount %d, got %d", cp.AttemptCount, imported.AttemptCount)
	}
}

func TestOpenCheckpointStore(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "open-checkpoint-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Test with a specific path instead of default
	store, err := OpenCheckpointStoreAt(tmpDir)
	if err != nil {
		t.Fatalf("failed to open checkpoint store: %v", err)
	}

	if store == nil {
		t.Fatal("expected non-nil store")
	}

	// Test that we can save and retrieve
	cp := CreateCheckpoint("pipeline-1", "stage-1", StageCanceled)
	cp.ID = "test-open-id"

	err = store.Save(cp)
	if err != nil {
		t.Fatalf("failed to save: %v", err)
	}

	got, ok := store.Get("test-open-id")
	if !ok {
		t.Error("expected to find checkpoint")
	}
	if got == nil {
		t.Error("expected non-nil checkpoint")
	}
	if got.ID != cp.ID {
		t.Errorf("expected ID %s, got %s", cp.ID, got.ID)
	}
}

func TestCheckpointStoreImportExportDirect(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "checkpoint-store-io-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// Create and save a checkpoint
	cp := CreateCheckpoint("pipeline-1", "stage-1", StageCanceled)
	cp.ID = "export-import-test"
	cp.Outputs = "test-output-hash"
	cp.Error = "test error"
	cp.AttemptCount = 5

	err = store.Save(cp)
	if err != nil {
		t.Fatalf("failed to save: %v", err)
	}

	// Export it
	var buf bytes.Buffer
	err = store.Export(&buf, cp.ID)
	if err != nil {
		t.Fatalf("failed to export: %v", err)
	}

	// Delete from store
	err = store.Delete(cp.ID)
	if err != nil {
		t.Fatalf("failed to delete: %v", err)
	}

	// Verify it's gone
	_, ok := store.Get(cp.ID)
	if ok {
		t.Error("expected checkpoint to be deleted")
	}

	// Import it back
	imported, err := store.Import(&buf)
	if err != nil {
		t.Fatalf("failed to import: %v", err)
	}

	if imported.ID != cp.ID {
		t.Errorf("expected ID %s, got %s", cp.ID, imported.ID)
	}
	if imported.Outputs != cp.Outputs {
		t.Errorf("expected Outputs %s, got %s", cp.Outputs, imported.Outputs)
	}
	if imported.Error != cp.Error {
		t.Errorf("expected Error %s, got %s", cp.Error, imported.Error)
	}
	if imported.AttemptCount != cp.AttemptCount {
		t.Errorf("expected AttemptCount %d, got %d", cp.AttemptCount, imported.AttemptCount)
	}
}

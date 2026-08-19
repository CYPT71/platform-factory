package provenance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	appmigration "github.com/CYPT71/platform-factory/internal/app/migration"
	"github.com/CYPT71/platform-factory/internal/core"
	domainmigration "github.com/CYPT71/platform-factory/internal/migration"
)

const migrationTestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func validMigrationEvidence(operation core.OperationID) appmigration.ExecutionEvidence {
	return appmigration.ExecutionEvidence{
		TraceID: "trace-1", InputDigest: migrationTestDigest, PlanDigest: migrationTestDigest,
		OperationID: operation, ResourceID: "resource-1", Capability: "migration.apply",
		CapabilityVersion: "v1", CandidateID: "plugin-b", CandidateDigest: migrationTestDigest,
		ResolvedCapability: "migration.apply", VerifiedCapability: "migration.apply",
		Compatibility:    appmigration.CompatibilityDirect,
		Gaps:             []domainmigration.CompatibilityGap{{ResourceID: "resource-1", Requirement: "migration.apply", Compatibility: domainmigration.CompatibilityDegraded, Reason: "feature unavailable"}},
		ObservationCount: 2, VerificationCount: 2, Verified: true, Applied: true,
		Status: appmigration.StepConverged,
	}
}

func TestMigrationExecutionStorePersistsReloadsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	store, err := NewMigrationExecutionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	evidence := validMigrationEvidence("migration-operation-1")
	if err := store.RecordExecution(context.Background(), evidence); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordExecution(context.Background(), evidence); err != nil {
		t.Fatalf("exact replay: %v", err)
	}

	reloaded, err := NewMigrationExecutionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	records := reloaded.Records()
	if len(records) != 1 {
		t.Fatalf("records=%d", len(records))
	}
	record := records[0]
	if record.FormatVersion != MigrationExecutionFormatVersion || record.TargetPluginID != "plugin-b" || !record.Verified || record.FinalStatus != appmigration.StepConverged {
		t.Fatalf("record=%+v", record)
	}
	if record.CanonicalGraphDigest != migrationTestDigest || record.PlanDigest != migrationTestDigest || record.ObservationCount != 2 || record.VerificationCount != 2 {
		t.Fatalf("missing observed facts: %+v", record)
	}

	conflict := evidence
	conflict.Applied = false
	if err := reloaded.RecordExecution(context.Background(), conflict); err == nil || !strings.Contains(err.Error(), "conflicting evidence") {
		t.Fatalf("conflicting replay err=%v", err)
	}
}

func TestMigrationExecutionStoreConcurrentRecordsHaveDeterministicOrder(t *testing.T) {
	store, err := NewMigrationExecutionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, id := range []core.OperationID{"migration-c", "migration-a", "migration-b"} {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.RecordExecution(context.Background(), validMigrationEvidence(id)); err != nil {
				t.Errorf("record %s: %v", id, err)
			}
		}()
	}
	wg.Wait()
	records := store.Records()
	if len(records) != 3 || records[0].OperationID != "migration-a" || records[1].OperationID != "migration-b" || records[2].OperationID != "migration-c" {
		t.Fatalf("records=%+v", records)
	}
}

func TestMigrationExecutionStoreConcurrentInstancesDoNotDuplicate(t *testing.T) {
	root := t.TempDir()
	first, err := NewMigrationExecutionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewMigrationExecutionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	evidence := validMigrationEvidence("migration-shared")
	errs := make(chan error, 2)
	go func() { errs <- first.RecordExecution(context.Background(), evidence) }()
	go func() { errs <- second.RecordExecution(context.Background(), evidence) }()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent append: %v", err)
		}
	}
	reloaded, err := NewMigrationExecutionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reloaded.Records()); got != 1 {
		t.Fatalf("records=%d", got)
	}
}

func TestMigrationExecutionStoreFailsClosedOnCorruptionAndIgnoresUnpublishedTemp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".migration-interrupted"), []byte(`{"partial":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMigrationExecutionStore(root); err != nil {
		t.Fatalf("unpublished temp should be ignored: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, strings.Repeat("a", 64)), []byte(`{"format_version":"bad"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMigrationExecutionStore(root); err == nil || !strings.Contains(err.Error(), "unsupported record format") {
		t.Fatalf("corrupt published record err=%v", err)
	}
}

func TestMigrationExecutionStoreRejectsSecretsAndFalseSuccess(t *testing.T) {
	store, err := NewMigrationExecutionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	secret := validMigrationEvidence("migration-secret")
	secret.CandidateID = "plugin-SECRET-SENTINEL"
	if err := store.RecordExecution(context.Background(), secret); err == nil || !strings.Contains(err.Error(), "secret-like") {
		t.Fatalf("secret evidence err=%v", err)
	}
	falseSuccess := validMigrationEvidence("migration-false-success")
	falseSuccess.Verified = false
	if err := store.RecordExecution(context.Background(), falseSuccess); err == nil || !strings.Contains(err.Error(), "convergence requires") {
		t.Fatalf("false convergence err=%v", err)
	}
	if got := len(store.Records()); got != 0 {
		t.Fatalf("persisted invalid records=%d", got)
	}
}

func TestMigrationExecutionStoreRejectsCancelledContext(t *testing.T) {
	store, err := NewMigrationExecutionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.RecordExecution(ctx, validMigrationEvidence("migration-cancelled")); err == nil {
		t.Fatal("cancelled persistence accepted")
	}
}

func TestMigrationWorkflowStorePersistsCompleteHostEvidence(t *testing.T) {
	root := t.TempDir()
	store, err := NewMigrationExecutionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	evidence := appmigration.WorkflowEvidence{
		TraceID: "trace-workflow", SourcePluginID: "plugin-a", SourcePluginDigest: migrationTestDigest, SourceCapability: "migration.discover",
		CanonicalGraphDigest: migrationTestDigest, PlanDigest: migrationTestDigest,
		SourceResourceIDs: []string{"database", "api"}, TargetResourceIDs: []string{"api", "database"},
		TargetPluginIDs: []string{"plugin-b"}, TargetPluginDigests: []string{migrationTestDigest},
		RequestedCapabilities: []string{"compute"}, ResolvedCapabilities: []string{"compute"}, VerifiedCapabilities: []string{"compute"},
		OperationIDs: []string{"migration-operation-b", "migration-operation-a"}, ObservationCount: 4, VerificationCount: 4, FinalState: "verified",
	}
	if err := store.RecordWorkflow(context.Background(), evidence); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWorkflow(context.Background(), evidence); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	reloaded, err := NewMigrationExecutionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	records := reloaded.WorkflowRecords()
	if len(records) != 1 || records[0].SourceResourceIDs[0] != "api" || records[0].OperationIDs[0] != "migration-operation-a" || records[0].FinalState != "verified" {
		t.Fatalf("records=%+v", records)
	}
	conflict := evidence
	conflict.FinalState = "failed"
	if err := reloaded.RecordWorkflow(context.Background(), conflict); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("conflict=%v", err)
	}
}

func TestMigrationWorkflowStoreRejectsFalseOrSecretEvidence(t *testing.T) {
	store, err := NewMigrationExecutionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := appmigration.WorkflowEvidence{TraceID: "trace", SourcePluginID: "plugin-a", SourcePluginDigest: migrationTestDigest, SourceCapability: "migration.discover", CanonicalGraphDigest: migrationTestDigest, PlanDigest: migrationTestDigest, SourceResourceIDs: []string{"a"}, TargetResourceIDs: []string{"a"}, TargetPluginIDs: []string{"plugin-b"}, TargetPluginDigests: []string{migrationTestDigest}, RequestedCapabilities: []string{"compute"}, ResolvedCapabilities: []string{"compute"}, VerifiedCapabilities: []string{"compute"}, OperationIDs: []string{"migration-operation-a"}, VerificationCount: 1, FinalState: "verified"}
	secret := base
	secret.SourcePluginID = "secret-sentinel"
	if err := store.RecordWorkflow(context.Background(), secret); err == nil {
		t.Fatal("secret workflow accepted")
	}
	falseClaim := base
	falseClaim.VerificationCount = 0
	if err := store.RecordWorkflow(context.Background(), falseClaim); err == nil {
		t.Fatal("unverified workflow accepted")
	}
}

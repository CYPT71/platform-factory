package provenance

import (
	"context"
	"strings"
	"testing"

	appmigration "github.com/CYPT71/platform-factory/internal/app/migration"
	"github.com/CYPT71/platform-factory/internal/core"
)

func verifiedArtifactEvidence(operation core.OperationID) appmigration.ArtifactEvidence {
	return appmigration.ArtifactEvidence{
		TraceID: "artifact-trace", OperationID: operation, ResourceID: "disk-1",
		Digest: migrationTestDigest, Size: 7, Format: appmigration.OCIArchiveFormat,
		SourcePluginID: "source-plugin", SourcePluginDigest: migrationTestDigest,
		TargetPluginID: "target-plugin", TargetPluginDigest: migrationTestDigest,
		ArtifactVerified: true, Imported: true, ObservedAfterImport: true, Verified: true,
	}
}

func TestMigrationExecutionStorePersistsArtifactFactsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	store, err := NewMigrationExecutionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	evidence := verifiedArtifactEvidence("migration-artifact-1")
	if err := store.RecordArtifact(context.Background(), evidence); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordArtifact(context.Background(), evidence); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewMigrationExecutionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	records := reloaded.ArtifactRecords()
	if len(records) != 1 {
		t.Fatalf("records=%d", len(records))
	}
	record := records[0]
	if record.FormatVersion != MigrationArtifactFormatVersion || record.Digest != migrationTestDigest || record.Size != 7 || record.Format != appmigration.OCIArchiveFormat {
		t.Fatalf("artifact identity facts=%+v", record)
	}
	if record.SourcePluginID != "source-plugin" || record.SourcePluginDigest != migrationTestDigest || record.TargetPluginID != "target-plugin" || record.TargetPluginDigest != migrationTestDigest {
		t.Fatalf("plugin identity facts=%+v", record)
	}
	if !record.ArtifactVerified || !record.Imported || !record.ObservedAfterImport || !record.Verified {
		t.Fatalf("verification facts=%+v", record)
	}
}

func TestMigrationExecutionStoreRejectsConflictingArtifactEvidence(t *testing.T) {
	store, err := NewMigrationExecutionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	evidence := verifiedArtifactEvidence("migration-artifact-conflict")
	if err := store.RecordArtifact(context.Background(), evidence); err != nil {
		t.Fatal(err)
	}
	conflict := evidence
	conflict.TargetPluginID = "other-target"
	if err := store.RecordArtifact(context.Background(), conflict); err == nil || !strings.Contains(err.Error(), "conflicting artifact evidence") {
		t.Fatalf("conflict err=%v", err)
	}
}

func TestMigrationExecutionStoreRejectsFalseArtifactSuccessAndSecrets(t *testing.T) {
	store, err := NewMigrationExecutionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, tc := range []struct {
		name   string
		mutate func(*appmigration.ArtifactEvidence)
	}{
		{"import without observation", func(e *appmigration.ArtifactEvidence) { e.ObservedAfterImport = false }},
		{"verify without import", func(e *appmigration.ArtifactEvidence) { e.Imported = false }},
		{"unverified artifact", func(e *appmigration.ArtifactEvidence) { e.ArtifactVerified = false }},
		{"raw secret sentinel", func(e *appmigration.ArtifactEvidence) { e.SourcePluginID = "SECRET-SENTINEL" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence := verifiedArtifactEvidence(core.OperationID("migration-artifact-" + strings.ReplaceAll(tc.name, " ", "-")))
			tc.mutate(&evidence)
			if err := store.RecordArtifact(context.Background(), evidence); err == nil {
				t.Fatal("invalid artifact success evidence persisted")
			}
		})
	}
	if len(store.ArtifactRecords()) != 0 {
		t.Fatal("invalid artifact records retained")
	}
}

func TestMigrationExecutionStorePersistsArtifactFailureWithoutInventingIdentity(t *testing.T) {
	root := t.TempDir()
	store, err := NewMigrationExecutionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	evidence := appmigration.ArtifactEvidence{TraceID: "trace-failed-export", OperationID: "migration-failed-export", ResourceID: "disk-1"}
	if err := store.RecordArtifact(context.Background(), evidence); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewMigrationExecutionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	records := reloaded.ArtifactRecords()
	if len(records) != 1 || records[0].Digest != "" || records[0].ArtifactVerified || records[0].Imported || records[0].Verified {
		t.Fatalf("failed evidence=%+v", records)
	}
}

func TestMigrationExecutionStorePersistsObservedButUnverifiedImportWithoutFalseSuccess(t *testing.T) {
	store, err := NewMigrationExecutionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	evidence := verifiedArtifactEvidence("migration-artifact-unverified-state")
	evidence.Imported = false
	evidence.Verified = false
	if err := store.RecordArtifact(context.Background(), evidence); err != nil {
		t.Fatal(err)
	}
	record := store.ArtifactRecords()[0]
	if !record.ArtifactVerified || !record.ObservedAfterImport || record.Imported || record.Verified {
		t.Fatalf("record=%+v", record)
	}
}

var _ appmigration.ArtifactEvidenceSink = (*MigrationExecutionStore)(nil)

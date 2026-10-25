package provenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	appmigration "github.com/CYPT71/platform-factory/internal/app/migration"
)

// Recovery must enforce the same trust boundary as live evidence submission:
// an on-disk edit must never turn missing facts into a verified migration.
func TestMigrationRecoveryRejectsForgedDurableEvidence(t *testing.T) {
	execution := recordFromEvidence(validMigrationEvidence("migration-recovery"))
	artifact := MigrationArtifactRecord{FormatVersion: MigrationArtifactFormatVersion, TraceID: "trace", OperationID: "migration-recovery", ResourceID: "database", Digest: migrationTestDigest, Format: "oci", SourcePluginID: "source", SourcePluginDigest: migrationTestDigest, TargetPluginID: "target", TargetPluginDigest: migrationTestDigest, ArtifactVerified: true, Imported: true, ObservedAfterImport: true, Verified: true}
	workflow := MigrationWorkflowRecord{FormatVersion: MigrationWorkflowFormatVersion, TraceID: "trace", SourcePluginID: "source", SourcePluginDigest: migrationTestDigest, SourceCapability: "migration.discover", CanonicalGraphDigest: migrationTestDigest, PlanDigest: migrationTestDigest, SourceResourceIDs: []string{"database"}, TargetResourceIDs: []string{"database"}, TargetPluginIDs: []string{"target"}, TargetPluginDigests: []string{migrationTestDigest}, OperationIDs: []string{"migration-recovery"}, VerifiedCapabilities: []string{"compute"}, VerificationCount: 1, FinalState: "verified"}
	type change struct {
		name, field string
		value       any
	}
	for _, suite := range []struct {
		name    string
		record  any
		key     string
		changes []change
	}{
		{"execution", execution, migrationExecutionKey(execution.TraceID, execution.OperationID), []change{
			{"format", "format_version", "unknown"}, {"missing resource", "resource_id", ""}, {"bad digest", "plan_digest", "sha256:bad"}, {"unknown outcome", "final_status", "succeeded"}, {"blocked execution", "final_status", "blocked"}, {"no verification", "verification_count", 0}, {"secret", "resource_id", "password=do-not-store"}, {"unknown field", "forged", true}, {"identity substitution", "trace_id", "another-trace"},
		}},
		{"artifact", artifact, migrationArtifactKey(artifact.TraceID, artifact.OperationID), []change{
			{"missing identity", "resource_id", ""}, {"negative size", "size", -1}, {"bad digest", "digest", "sha256:bad"}, {"unpaired identity", "format", ""}, {"plugin digest", "source_plugin_digest", "bad"}, {"unpaired plugin", "source_plugin_id", ""}, {"no artifact verification", "artifact_verified", false}, {"no import observation", "observed_after_import", false}, {"no import", "imported", false}, {"unknown field", "forged", true}, {"identity substitution", "trace_id", "another-trace"},
		}},
		{"workflow", workflow, migrationWorkflowKey(workflow.TraceID, workflow.PlanDigest), []change{
			{"missing identity", "source_plugin_id", ""}, {"unknown outcome", "final_state", "succeeded"}, {"missing resources", "source_resource_ids", []string{}}, {"missing plugins", "target_plugin_ids", []string{}}, {"no verification", "verification_count", 0}, {"duplicate operation", "operation_ids", []string{"migration-recovery", "migration-recovery"}}, {"bad plugin digest", "target_plugin_digests", []string{"bad"}}, {"unknown field", "forged", true}, {"identity substitution", "trace_id", "another-trace"},
		}},
	} {
		for _, mutation := range suite.changes {
			t.Run(suite.name+"/"+mutation.name, func(t *testing.T) {
				raw, err := json.Marshal(suite.record)
				if err != nil {
					t.Fatal(err)
				}
				var document map[string]any
				if err := json.Unmarshal(raw, &document); err != nil {
					t.Fatal(err)
				}
				document[mutation.field] = mutation.value
				raw, err = json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				root := t.TempDir()
				path := filepath.Join(root, suite.key)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				store, err := NewMigrationExecutionStore(root)
				if err == nil {
					store.Close()
					t.Fatal("forged recovery evidence accepted")
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatal("recovery destroyed evidence needed for investigation")
				}
			})
		}
	}
}

func TestMigrationStoreClosureAndCancellationPreserveEvidence(t *testing.T) {
	root := t.TempDir()
	store, err := NewMigrationExecutionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	evidence := validMigrationEvidence("migration-closed")
	if err := store.RecordExecution(context.Background(), evidence); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordExecution(context.Background(), validMigrationEvidence("migration-late")); err == nil {
		t.Fatal("write to closed store accepted")
	}
	reopened, err := NewMigrationExecutionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(reopened.Records()) != 1 {
		t.Fatal("closed write damaged committed evidence")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := reopened.RecordArtifact(ctx, appmigration.ArtifactEvidence{}); err == nil {
		t.Fatal("cancelled artifact accepted")
	}
	if err := reopened.RecordWorkflow(ctx, appmigration.WorkflowEvidence{}); err == nil {
		t.Fatal("cancelled workflow accepted")
	}
	for _, path := range []string{"", filepath.Join(root, "not-directory", "nested")} {
		if path != "" {
			if err := os.WriteFile(filepath.Join(root, "not-directory"), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if s, err := NewMigrationExecutionStore(path); err == nil {
			s.Close()
			t.Fatal("invalid store root accepted")
		}
	}
}

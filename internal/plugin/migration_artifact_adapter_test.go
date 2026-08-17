package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	appmigration "github.com/CYPT71/platform-factory/internal/app/migration"
)

func TestMigrationArtifactFactoryRequiresExactRuntimeResourceScope(t *testing.T) {
	r, _ := migrationTestRegistry(t, map[string]testHandler{migrationExportCapability: func(context.Context, json.RawMessage) (any, error) { return migrationExportResult{}, nil }})
	state := r.states["migration-test"]
	state.verifiedManifest.Permissions.Network = []string{"egress"}
	r.states["migration-test"] = state
	_, err := (MigrationArtifactFactory{Registry: r}).OpenExporter(context.Background(), appmigration.ResolvedCapability{CandidateID: "migration-test", Digest: migrationTestDigest}, "")
	if err == nil || !strings.Contains(err.Error(), "resource") {
		t.Fatalf("err=%v", err)
	}
}

func TestMigrationArtifactAdaptersReResolveRevocation(t *testing.T) {
	calls := 0
	r, _ := migrationTestRegistry(t, map[string]testHandler{migrationExportCapability: func(context.Context, json.RawMessage) (any, error) { calls++; return migrationExportResult{}, nil }, migrationImportCapability: func(context.Context, json.RawMessage) (any, error) {
		calls++
		return migrationImportResult{Accepted: true}, nil
	}, migrationArtifactObserveCapability: func(context.Context, json.RawMessage) (any, error) { return migrationArtifactObserveResult{}, nil }})
	f := MigrationArtifactFactory{Registry: r}
	resolved := appmigration.ResolvedCapability{CandidateID: "migration-test", Digest: migrationTestDigest}
	exporter, err := f.OpenExporter(context.Background(), resolved, "vm-1")
	if err != nil {
		t.Fatal(err)
	}
	importer, err := f.OpenImporter(context.Background(), resolved, "vm-1")
	if err != nil {
		t.Fatal(err)
	}
	wrong := migrationResource()
	wrong.ID = "other"
	if _, err := exporter.Export(context.Background(), wrong); err == nil {
		t.Fatal("export exceeded grant resource scope")
	}
	if calls != 0 {
		t.Fatalf("out-of-scope dispatch calls=%d", calls)
	}
	r.Unregister("migration-test")
	if _, err := exporter.Export(context.Background(), migrationResource()); err == nil {
		t.Fatal("revoked exporter dispatched")
	}
	if err := importer.Import(context.Background(), "operation", migrationResource(), appmigration.VerifiedArtifact{}); err == nil {
		t.Fatal("revoked importer dispatched")
	}
	if calls != 0 {
		t.Fatalf("calls=%d", calls)
	}
}
func TestArtifactVerificationRejectsResourceOnlyMaterialization(t *testing.T) {
	encoded, err := json.Marshal(migrationArtifactObserveParams{Resource: resourceToWire(migrationResource())})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"digest", "size", "format"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("artifact expectation leaked: %s", encoded)
		}
	}
}
func TestEffectiveArtifactGrantIsStructuredAndOperationScoped(t *testing.T) {
	r, _ := migrationTestRegistry(t, map[string]testHandler{migrationExportCapability: func(context.Context, json.RawMessage) (any, error) { return nil, nil }, migrationImportCapability: func(context.Context, json.RawMessage) (any, error) { return nil, nil }})
	export, err := r.effectiveArtifactGrant(context.Background(), "migration-test", migrationTestDigest, migrationExportCapability, "resource")
	if err != nil {
		t.Fatal(err)
	}
	imp, err := r.effectiveArtifactGrant(context.Background(), "migration-test", migrationTestDigest, migrationImportCapability, "resource")
	if err != nil {
		t.Fatal(err)
	}
	if export.ResourceID != "resource" || export.Capability == imp.Capability || export.EvidenceDigest == imp.EvidenceDigest || export.EffectiveIsolation != "test-enforced-sandbox" || len(export.NetworkScopes)+len(export.FilesystemScopes)+len(export.CredentialScopes) != 0 {
		t.Fatalf("export=%+v import=%+v", export, imp)
	}
}
func TestEffectiveArtifactGrantAlwaysRejectsNoIsolation(t *testing.T) {
	r, _ := migrationTestRegistry(t, map[string]testHandler{migrationExportCapability: func(context.Context, json.RawMessage) (any, error) { return nil, nil }})
	state := r.states["migration-test"]
	state.client.runtimeGrantEvidence = newRuntimeGrantEvidence("none", nil, nil, nil, "runtime")
	r.states["migration-test"] = state
	if _, err := r.effectiveArtifactGrant(context.Background(), "migration-test", migrationTestDigest, migrationExportCapability, "resource"); err == nil {
		t.Fatal("isolation none received artifact grant")
	}
}

package plugin

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appmigration "github.com/CYPT71/platform-factory/internal/app/migration"
	"github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/idempotency"
	domainmigration "github.com/CYPT71/platform-factory/internal/migration"
	"github.com/CYPT71/platform-factory/internal/observability"
	"github.com/CYPT71/platform-factory/internal/provenance"
)

func startBidirectionalFixture(t *testing.T, name string, initial bool) (*Registry, Manifest, *Client) {
	t.Helper()
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "fixture-built")
	initialValue := "0"
	if initial {
		initialValue = "1"
	}
	ldflags := strings.Join([]string{"-X", "main.configuredName=" + name, "-X", "main.configuredBidirectional=1", "-X", "main.configuredInitial=" + initialValue}, " ")
	cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", binary, ".")
	cmd.Dir = filepath.Join("..", "..", "testdata", "plugins", "migration")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod -buildvcs=false", "GOPROXY=off", "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v: %s", name, err, output)
	}
	payload, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	dir := filepath.Join(tmp, "installed")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin"), payload, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{APIVersion: ManifestAPIVersion, Name: name, Version: "v1", Family: PluginFamilyCapability, Capabilities: []string{migrationDiscoverCapability, migrationInspectCapability, migrationObserveCapability, migrationApplyCapability, migrationExportCapability, migrationImportCapability, migrationArtifactObserveCapability}, Executable: "plugin", Digest: "sha256:" + hex.EncodeToString(sum[:])}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Sign(private, name+"-key"); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	registry.registerDiscovered(manifest)
	client, err := VerifyStartAndPublishAvailableWithJournal(context.Background(), registry, dir, manifest, TrustPolicy{Keys: []ed25519.PublicKey{public}, AllowUnsandboxedExecution: true}, idempotency.NewMemoryJournal())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return registry, manifest, client
}

func TestExternalBidirectionalPluginsRoundTripBothDirections(t *testing.T) {
	for _, direction := range []struct{ source, target string }{{"plugin-a", "plugin-b"}, {"plugin-b", "plugin-a"}} {
		t.Run(direction.source+"-to-"+direction.target, func(t *testing.T) {
			sourceRegistry, sourceManifest, sourceClient := startBidirectionalFixture(t, direction.source, true)
			targetRegistry, targetManifest, targetClient := startBidirectionalFixture(t, direction.target, false)
			sourceClient.runtimeGrantEvidence = newRuntimeGrantEvidence("test-enforced-sandbox", nil, nil, nil, "external-round-trip")
			targetClient.runtimeGrantEvidence = newRuntimeGrantEvidence("test-enforced-sandbox", nil, nil, nil, "external-round-trip")
			store, err := provenance.NewMigrationExecutionStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			executor := appmigration.NewExecutor(appmigration.NewCapabilityResolver(targetRegistry), MigrationTargetFactory{Registry: targetRegistry}, store)
			validator := appmigration.NewRoundTripValidatorWithProvenance(appmigration.NewDiscoverer(), appmigration.NewPlanner(), executor, store)
			ctx := observability.ContextWithTraceID(context.Background(), "external-"+direction.source+"-to-"+direction.target)
			result, err := validator.Validate(ctx, NewMigrationDiscoverySource(sourceRegistry, sourceManifest.Name, sourceManifest.Digest), NewMigrationDiscoverySource(targetRegistry, targetManifest.Name, targetManifest.Digest))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Source.Resources) != 1 || len(result.Target.Resources) != 1 || len(result.Execution.Evidence) != 1 || !result.Execution.Evidence[0].Applied || !result.Execution.Evidence[0].Verified {
				t.Fatalf("result=%+v", result)
			}
			inspected, found, err := NewMigrationDiscoverySource(targetRegistry, targetManifest.Name, targetManifest.Digest).Inspect(ctx, "vm-1")
			if err != nil || !found || inspected.ID != "vm-1" {
				t.Fatalf("inspect=%+v found=%v err=%v", inspected, found, err)
			}
			artifactFactory := MigrationArtifactFactory{Registry: sourceRegistry}
			exporter, err := artifactFactory.OpenExporter(ctx, appmigration.ResolvedCapability{CandidateID: sourceManifest.Name, Digest: sourceManifest.Digest}, "vm-1")
			if err != nil {
				t.Fatal(err)
			}
			importer, err := (MigrationArtifactFactory{Registry: targetRegistry}).OpenImporter(ctx, appmigration.ResolvedCapability{CandidateID: targetManifest.Name, Digest: targetManifest.Digest}, "vm-1")
			if err != nil {
				t.Fatal(err)
			}
			artifact, err := exporter.Export(ctx, result.Source.Resources[0])
			if err != nil {
				t.Fatal(err)
			}
			verifiedArtifact := appmigration.VerifiedArtifact{Digest: artifact.Digest, Size: artifact.Size, Format: artifact.Format, Data: artifact.Data}
			if err := importer.Import(ctx, core.OperationID("migration-artifact-"+direction.source+"-to-"+direction.target), result.Source.Resources[0], verifiedArtifact); err != nil {
				t.Fatal(err)
			}
			artifactObservation, err := importer.ObserveArtifact(ctx, result.Source.Resources[0])
			if err != nil || !artifactObservation.Found || artifactObservation.NativeBinding == "" || !strings.Contains(string(artifactObservation.Attestation), artifact.Digest) {
				t.Fatalf("artifact observation=%+v err=%v", artifactObservation, err)
			}
			workflows := store.WorkflowRecords()
			if len(workflows) != 1 || workflows[0].SourcePluginID != direction.source || len(workflows[0].TargetPluginIDs) != 1 || workflows[0].TargetPluginIDs[0] != direction.target || workflows[0].FinalState != "verified" {
				t.Fatalf("workflows=%+v", workflows)
			}
		})
	}
}

func TestExternalMigrationPluginCrashFailsClosed(t *testing.T) {
	registry, manifest, client := startBidirectionalFixture(t, "crash-plugin", true)
	source := NewMigrationDiscoverySource(registry, manifest.Name, manifest.Digest)
	target, err := (MigrationTargetFactory{Registry: registry}).Open(context.Background(), appmigration.ResolvedCapability{CandidateID: manifest.Name, Digest: manifest.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := observability.ContextWithTraceID(context.Background(), "plugin-crash")
	if _, err := source.DiscoverPage(ctx, ""); err == nil {
		t.Fatal("crashed source plugin reported discovery success")
	}
	resource := domainmigration.Resource{ID: "vm-1", Kind: "compute", Origin: domainmigration.ResourceOrigin{Source: manifest.Name, NativeType: "vm", NativeID: "one"}, Requirements: []domainmigration.Requirement{{Capability: migrationApplyCapability, Version: "v1"}}}
	if _, err := target.Observe(ctx, resource); err == nil {
		t.Fatal("crashed target plugin reported observation success")
	}
	if err := target.Apply(ctx, domainmigration.Step{OperationID: "crash-operation", ResourceID: resource.ID, Capability: migrationApplyCapability, Version: "v1", Action: "apply"}, resource); err == nil {
		t.Fatal("crashed target plugin reported mutation success")
	}
}

func TestExternalSDKMigrationPluginSignedOffline(t *testing.T) {
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "migration-fixture")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = filepath.Join("..", "..", "testdata", "plugins", "migration")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod -buildvcs=false", "GOPROXY=off", "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build external migration plugin: %v: %s", err, output)
	}
	payload, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	dir := filepath.Join(tmp, "installed")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(dir, "plugin")
	if err := os.WriteFile(installed, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{APIVersion: ManifestAPIVersion, Name: "migration-fixture", Version: "v1", Family: PluginFamilyCapability, Capabilities: []string{migrationDiscoverCapability, migrationInspectCapability, migrationObserveCapability, migrationApplyCapability, migrationExportCapability, migrationImportCapability, migrationArtifactObserveCapability}, Executable: "plugin", Digest: "sha256:" + hex.EncodeToString(sum[:])}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Sign(private, "fixture"); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	r.registerDiscovered(manifest)
	client, err := VerifyStartAndPublishAvailableWithJournal(context.Background(), r, dir, manifest, TrustPolicy{Keys: []ed25519.PublicKey{public}, AllowUnsandboxedExecution: true}, idempotency.NewMemoryJournal())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := observability.ContextWithTraceID(context.Background(), "trace-external")
	aggregate, err := appmigration.NewDiscoverer().Discover(ctx, NewMigrationDiscoverySource(r, manifest.Name, manifest.Digest))
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Resources) != 1 || len(aggregate.Unknowns) != 1 {
		t.Fatalf("aggregate=%+v", aggregate)
	}
	target, err := (MigrationTargetFactory{Registry: r}).Open(ctx, appmigration.ResolvedCapability{CandidateID: manifest.Name, Digest: manifest.Digest})
	if err != nil {
		t.Fatal(err)
	}
	step := domainmigration.Step{OperationID: core.OperationID("external-operation"), ResourceID: "vm-1", Capability: migrationApplyCapability, Version: "v1", Action: "create"}
	if err := target.Apply(ctx, step, aggregate.Resources[0]); err != nil {
		t.Fatal(err)
	}
	observed, err := target.Observe(ctx, aggregate.Resources[0])
	if err != nil {
		t.Fatal(err)
	}
	verified, err := target.Verify(ctx, aggregate.Resources[0], observed)
	if err != nil || !verified {
		t.Fatalf("verified=%v err=%v", verified, err)
	}
	if err := target.Apply(ctx, step, aggregate.Resources[0]); err == nil {
		t.Fatal("duplicate operation unexpectedly dispatched")
	}
	artifactResource := aggregate.Resources[0]
	artifactResource.ID = "artifact-vm"
	artifactResource.Origin.NativeID = "artifact-one"
	factory := MigrationArtifactFactory{Registry: r}
	if client.runtimeGrantEvidence == nil {
		if _, err := factory.OpenExporter(ctx, appmigration.ResolvedCapability{CandidateID: manifest.Name, Digest: manifest.Digest}, artifactResource.ID); err == nil {
			t.Fatal("unsandboxed external artifact plugin received a grant")
		}
		return
	}
	t.Skip("sandboxed external artifact success requires an authorized classification authority")
}

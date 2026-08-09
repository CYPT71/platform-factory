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
	"testing"

	appmigration "github.com/CYPT71/secure-oci-base/internal/app/migration"
	"github.com/CYPT71/secure-oci-base/internal/core"
	"github.com/CYPT71/secure-oci-base/internal/idempotency"
	domainmigration "github.com/CYPT71/secure-oci-base/internal/migration"
	"github.com/CYPT71/secure-oci-base/internal/observability"
)

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
	manifest := Manifest{APIVersion: ManifestAPIVersion, Name: "migration-fixture", Version: "v1", Family: PluginFamilyCapability, Capabilities: []string{migrationDiscoverCapability, migrationObserveCapability, migrationApplyCapability, migrationExportCapability, migrationImportCapability, migrationArtifactObserveCapability}, Executable: "plugin", Digest: "sha256:" + hex.EncodeToString(sum[:])}
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

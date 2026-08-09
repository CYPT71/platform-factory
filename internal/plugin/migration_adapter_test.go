package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	appmigration "github.com/CYPT71/secure-oci-base/internal/app/migration"
	"github.com/CYPT71/secure-oci-base/internal/core"
	"github.com/CYPT71/secure-oci-base/internal/idempotency"
	domainmigration "github.com/CYPT71/secure-oci-base/internal/migration"
	"github.com/CYPT71/secure-oci-base/internal/observability"
)

const migrationTestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func migrationTestRegistry(t *testing.T, handlers map[string]testHandler) (*Registry, *Client) {
	t.Helper()
	server := &testServer{name: "migration-test", version: "v1", handlers: handlers}
	conn := newPipeConn()
	serveInBackground(t, server, conn)
	client, err := attachClient(t, conn)
	if err != nil {
		t.Fatal(err)
	}
	client.verifiedDigest = migrationTestDigest
	client.runtimeGrantEvidence = newRuntimeGrantEvidence("test-enforced-sandbox", nil, nil, nil, "test-runtime")
	client.journal = idempotency.NewMemoryJournal()
	manifest := Manifest{Name: "migration-test", Version: "v1", Digest: migrationTestDigest, Capabilities: client.Hello().Capabilities}
	r := NewRegistry()
	r.plugins[manifest.Name] = manifest
	r.byCap = map[string][]string{}
	for _, c := range manifest.Capabilities {
		r.byCap[c] = []string{manifest.Name}
	}
	r.states[manifest.Name] = capabilityState{declared: true, discovered: true, negotiated: true, verified: true, client: client, verifiedManifest: manifest}
	return r, client
}

func migrationResource() domainmigration.Resource {
	return domainmigration.Resource{ID: "vm-1", Kind: "compute", Origin: domainmigration.ResourceOrigin{Source: "migration-test", NativeType: "vm", NativeID: "one"}, Attributes: map[string]string{"cpu": "2"}, Requirements: []domainmigration.Requirement{{Capability: "migration.apply", Version: "v1"}}}
}

func TestMigrationDiscoveryAdapterNormalizesPaginationAndFailsClosed(t *testing.T) {
	r, _ := migrationTestRegistry(t, map[string]testHandler{migrationDiscoverCapability: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params migrationDiscoverParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		if params.Cursor == "" {
			return migrationDiscoverResult{Status: "complete", Resources: []migrationWireResource{resourceToWire(migrationResource())}, NextCursor: "two"}, nil
		}
		return migrationDiscoverResult{Status: "partial", Unknowns: []migrationWireUnknown{{Source: "migration-test", Kind: "permission-denied", Scope: "network", Reason: "denied"}}}, nil
	}})
	source := NewMigrationDiscoverySource(r, "migration-test", migrationTestDigest)
	aggregate, err := appmigration.NewDiscoverer().Discover(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Discovery != domainmigration.DiscoveryPartial || len(aggregate.Resources) != 1 || len(aggregate.Unknowns) != 1 {
		t.Fatalf("aggregate=%+v", aggregate)
	}
	bad := NewMigrationDiscoverySource(r, "migration-test", "sha256:"+strings.Repeat("b", 64))
	if _, err := bad.DiscoverPage(context.Background(), ""); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.DiscoverPage(ctx, ""); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestMigrationTargetApplyObserveVerifyUsesJournal(t *testing.T) {
	desired := migrationResource()
	state := domainmigration.Resource{}
	calls := 0
	r, _ := migrationTestRegistry(t, map[string]testHandler{
		migrationObserveCapability: func(_ context.Context, _ json.RawMessage) (any, error) {
			if state.ID == "" {
				return migrationObserveResult{}, nil
			}
			w := resourceToWire(state)
			return migrationObserveResult{Found: true, Resource: &w}, nil
		},
		migrationApplyCapability: func(_ context.Context, raw json.RawMessage) (any, error) {
			calls++
			var p migrationApplyParams
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			state = resourceFromWire(p.Resource)
			return migrationApplyResult{Accepted: true}, nil
		},
	})
	factory := MigrationTargetFactory{Registry: r}
	resolved := appmigration.ResolvedCapability{CandidateID: "migration-test", Digest: migrationTestDigest}
	target, err := factory.Open(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	step := domainmigration.Step{OperationID: core.OperationID("operation-1"), ResourceID: desired.ID, Capability: migrationApplyCapability, Version: "v1", Action: "create"}
	ctx := observability.ContextWithTraceID(context.Background(), "trace-1")
	if err := target.Apply(ctx, step, desired); err != nil {
		t.Fatal(err)
	}
	observation, err := target.Observe(ctx, desired)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := target.Verify(ctx, desired, observation)
	if err != nil || !ok {
		t.Fatalf("verified=%v err=%v", ok, err)
	}
	if err := target.Apply(ctx, step, desired); err == nil || !strings.Contains(err.Error(), "replayable result") {
		t.Fatalf("duplicate err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("apply calls=%d", calls)
	}
}

func TestMigrationTargetRejectsMalformedObservation(t *testing.T) {
	r, _ := migrationTestRegistry(t, map[string]testHandler{
		migrationObserveCapability: func(context.Context, json.RawMessage) (any, error) { return migrationObserveResult{Found: true}, nil },
		migrationApplyCapability:   func(context.Context, json.RawMessage) (any, error) { return migrationApplyResult{Accepted: true}, nil },
	})
	target, err := (MigrationTargetFactory{Registry: r}).Open(context.Background(), appmigration.ResolvedCapability{CandidateID: "migration-test", Digest: migrationTestDigest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Observe(context.Background(), migrationResource()); err == nil {
		t.Fatal("malformed observation accepted")
	}
}

func TestMigrationTargetRejectsSpoofedObservationSource(t *testing.T) {
	spoof := resourceToWire(migrationResource())
	spoof.Origin.Source = "spoof"
	r, _ := migrationTestRegistry(t, map[string]testHandler{
		migrationObserveCapability: func(context.Context, json.RawMessage) (any, error) {
			return migrationObserveResult{Found: true, Resource: &spoof}, nil
		},
		migrationApplyCapability: func(context.Context, json.RawMessage) (any, error) { return migrationApplyResult{Accepted: true}, nil },
	})
	target, err := (MigrationTargetFactory{Registry: r}).Open(context.Background(), appmigration.ResolvedCapability{CandidateID: "migration-test", Digest: migrationTestDigest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Observe(context.Background(), migrationResource()); err == nil {
		t.Fatal("spoofed observation source accepted")
	}
}

func TestPublishAvailableWithJournalFailsClosedWithoutJournal(t *testing.T) {
	if _, err := VerifyStartAndPublishAvailableWithJournal(context.Background(), NewRegistry(), t.TempDir(), Manifest{Name: "target"}, TrustPolicy{}, nil); err == nil || !strings.Contains(err.Error(), "journal") {
		t.Fatalf("err=%v", err)
	}
}

func TestMigrationAdaptersRefuseAfterUnregisterWithoutDispatch(t *testing.T) {
	var calls int
	r, _ := migrationTestRegistry(t, map[string]testHandler{
		migrationDiscoverCapability: func(context.Context, json.RawMessage) (any, error) {
			calls++
			return migrationDiscoverResult{Status: "complete"}, nil
		},
		migrationObserveCapability: func(context.Context, json.RawMessage) (any, error) { calls++; return migrationObserveResult{}, nil },
		migrationApplyCapability: func(context.Context, json.RawMessage) (any, error) {
			calls++
			return migrationApplyResult{Accepted: true}, nil
		},
	})
	source := NewMigrationDiscoverySource(r, "migration-test", migrationTestDigest)
	target, err := (MigrationTargetFactory{Registry: r}).Open(context.Background(), appmigration.ResolvedCapability{CandidateID: "migration-test", Digest: migrationTestDigest})
	if err != nil {
		t.Fatal(err)
	}
	r.Unregister("migration-test")
	if _, err := source.DiscoverPage(context.Background(), ""); err == nil {
		t.Fatal("discovery used revoked client")
	}
	if _, err := target.Observe(context.Background(), migrationResource()); err == nil {
		t.Fatal("observe used revoked client")
	}
	if err := target.Apply(context.Background(), domainmigration.Step{OperationID: "stale", ResourceID: "vm-1", Capability: migrationApplyCapability}, migrationResource()); err == nil {
		t.Fatal("apply used revoked client")
	}
	if calls != 0 {
		t.Fatalf("revoked plugin dispatched %d calls", calls)
	}
}

func TestMigrationDiscoveryRejectsSpoofedSources(t *testing.T) {
	for _, result := range []migrationDiscoverResult{
		{Status: "complete", Resources: []migrationWireResource{{ID: "r", Kind: "vm", Origin: migrationWireOrigin{Source: "spoof", NativeType: "vm", NativeID: "1"}}}},
		{Status: "partial", Unknowns: []migrationWireUnknown{{Source: "spoof", Kind: "denied", Scope: "all", Reason: "denied"}}},
	} {
		r, _ := migrationTestRegistry(t, map[string]testHandler{migrationDiscoverCapability: func(context.Context, json.RawMessage) (any, error) { return result, nil }})
		if _, err := NewMigrationDiscoverySource(r, "migration-test", migrationTestDigest).DiscoverPage(context.Background(), ""); err == nil {
			t.Fatal("spoofed source accepted")
		}
	}
}

package plugin

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRegisterMigrationSupportsIndependentCapabilitiesAndRequestIDs(t *testing.T) {
	server := NewServer("source", "v1")
	RegisterMigration(server, func(ctx context.Context, params MigrationDiscoverParams) (MigrationDiscoverResult, error) {
		if TraceIDFromContext(ctx) != "trace-1" || OperationIDFromContext(ctx) != "" || params.Cursor != "next" {
			t.Fatalf("request context or params not propagated: trace=%q operation=%q params=%+v", TraceIDFromContext(ctx), OperationIDFromContext(ctx), params)
		}
		return MigrationDiscoverResult{Status: "partial", Unknowns: []MigrationUnknownObservation{{Source: "source", Kind: "permission-denied", Scope: "network", Reason: "read denied"}}}, nil
	}, nil, nil)
	if len(server.capabilities) != 1 || server.capabilities[0] != CapabilityMigrationDiscover {
		t.Fatalf("capabilities=%v", server.capabilities)
	}
	raw, _ := json.Marshal(MigrationDiscoverParams{Cursor: "next"})
	resp := server.dispatch(context.Background(), Request{ID: "1", Method: "v1.migration.discover", Params: raw, TraceID: "trace-1"})
	if resp.Error != nil || resp.TraceID != "trace-1" {
		t.Fatalf("response=%+v", resp)
	}
}

func TestMigrationHandlerRejectsMissingAndMalformedParameters(t *testing.T) {
	handler := migrationHandler(func(context.Context, MigrationDiscoverParams) (MigrationDiscoverResult, error) {
		return MigrationDiscoverResult{}, nil
	})
	for _, raw := range []json.RawMessage{
		nil,
		json.RawMessage(`{`),
		json.RawMessage(`{"cursor":"next","provider_secret":"leak"}`),
		json.RawMessage(`{"cursor":"next"}{"cursor":"again"}`),
		json.RawMessage(`{"cursor":42}`),
	} {
		if _, err := handler(context.Background(), raw); err == nil {
			t.Fatalf("raw=%q accepted", raw)
		}
	}
}

func TestMigrationWireUsesStableSnakeCase(t *testing.T) {
	encoded, err := json.Marshal(MigrationApplyParams{Step: MigrationStep{OperationID: "op", ResourceID: "r", Capability: CapabilityMigrationApply, Action: "create"}, Resource: MigrationResource{ID: "r", Kind: "vm", Origin: MigrationResourceOrigin{Source: "s", NativeType: "machine", NativeID: "n"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"step":{"operation_id":"op","resource_id":"r","capability":"migration.apply","action":"create"},"resource":{"id":"r","kind":"vm","origin":{"source":"s","native_type":"machine","native_id":"n"}}}`
	if string(encoded) != want {
		t.Fatalf("wire=%s", encoded)
	}
}

func TestRegisterMigrationInspectHandlesAndIgnoresNilArgs(t *testing.T) {
	RegisterMigrationInspect(nil, nil) // must not panic

	server := NewServer("source", "v1")
	RegisterMigrationInspect(server, nil)
	if len(server.capabilities) != 0 {
		t.Fatalf("a nil inspect func must not register a handler: %v", server.capabilities)
	}

	RegisterMigrationInspect(server, func(context.Context, MigrationInspectParams) (MigrationInspectResult, error) {
		return MigrationInspectResult{Found: true, Resource: &MigrationResource{ID: "r"}}, nil
	})
	if len(server.capabilities) != 1 || server.capabilities[0] != CapabilityMigrationInspect {
		t.Fatalf("capabilities=%v", server.capabilities)
	}
	raw, _ := json.Marshal(MigrationInspectParams{ResourceID: "r"})
	resp := server.dispatch(context.Background(), Request{ID: "1", Method: "v1.migration.inspect", Params: raw})
	if resp.Error != nil {
		t.Fatalf("response=%+v", resp)
	}
}

func TestRegisterMigrationArtifactsRegistersEachIndependentCapability(t *testing.T) {
	RegisterMigrationArtifacts(nil, nil, nil, nil) // must not panic

	server := NewServer("target", "v1")
	RegisterMigrationArtifacts(server,
		func(context.Context, MigrationExportParams) (MigrationExportResult, error) {
			return MigrationExportResult{Artifact: MigrationArtifact{Digest: "sha256:x"}}, nil
		},
		func(context.Context, MigrationImportParams) (MigrationImportResult, error) {
			return MigrationImportResult{Accepted: true}, nil
		},
		func(context.Context, MigrationArtifactObserveParams) (MigrationArtifactObserveResult, error) {
			return MigrationArtifactObserveResult{Found: true}, nil
		},
	)
	want := []string{CapabilityMigrationExport, CapabilityMigrationImport, CapabilityMigrationArtifactObserve}
	if len(server.capabilities) != len(want) {
		t.Fatalf("capabilities=%v, want %v", server.capabilities, want)
	}
	for i, capability := range want {
		if server.capabilities[i] != capability {
			t.Fatalf("capabilities=%v, want %v", server.capabilities, want)
		}
	}

	raw, _ := json.Marshal(MigrationExportParams{Resource: MigrationResource{ID: "r"}})
	resp := server.dispatch(context.Background(), Request{ID: "1", Method: "v1.migration.export", Params: raw})
	if resp.Error != nil {
		t.Fatalf("export response=%+v", resp)
	}
}

func TestMigrationApplyReceivesOperationID(t *testing.T) {
	server := NewServer("target", "v1")
	RegisterMigration(server, nil, nil, func(ctx context.Context, _ MigrationApplyParams) (MigrationApplyResult, error) {
		if TraceIDFromContext(ctx) != "trace" || OperationIDFromContext(ctx) != "operation" {
			t.Fatalf("missing request IDs")
		}
		return MigrationApplyResult{Accepted: true}, nil
	})
	raw, _ := json.Marshal(MigrationApplyParams{})
	resp := server.dispatch(context.Background(), Request{ID: "1", Method: "v1.migration.apply", Params: raw, TraceID: "trace", OperationID: "operation"})
	if resp.Error != nil || resp.OperationID != "operation" {
		t.Fatalf("response=%+v", resp)
	}
}

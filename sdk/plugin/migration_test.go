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

package plugin

import (
	"context"
	"encoding/json"
	"errors"
)

// MigrationResource is the provider-neutral v1 wire representation. It is
// intentionally owned by the SDK and contains neither credentials nor plugin
// selection information.
type MigrationResource struct {
	ID           string                  `json:"id"`
	Kind         string                  `json:"kind"`
	Origin       MigrationResourceOrigin `json:"origin"`
	Attributes   map[string]string       `json:"attributes,omitempty"`
	Requirements []MigrationRequirement  `json:"requirements,omitempty"`
}

type MigrationResourceOrigin struct {
	Source     string `json:"source"`
	NativeType string `json:"native_type"`
	NativeID   string `json:"native_id"`
}
type MigrationRequirement struct {
	Capability string `json:"capability"`
	Version    string `json:"version,omitempty"`
}
type MigrationDependencyEdge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation"`
	Required bool   `json:"required"`
}
type MigrationUnknownObservation struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Scope  string `json:"scope"`
	Reason string `json:"reason"`
}
type MigrationCompatibilityGap struct {
	ResourceID       string `json:"resource_id"`
	Requirement      string `json:"requirement"`
	Compatibility    string `json:"compatibility"`
	Reason           string `json:"reason"`
	LostGuarantee    string `json:"lost_guarantee,omitempty"`
	RequiresApproval bool   `json:"requires_approval,omitempty"`
}

type MigrationDiscoverParams struct {
	Cursor string `json:"cursor,omitempty"`
}
type MigrationDiscoverResult struct {
	Status     string                        `json:"status"`
	Resources  []MigrationResource           `json:"resources,omitempty"`
	Edges      []MigrationDependencyEdge     `json:"edges,omitempty"`
	Unknowns   []MigrationUnknownObservation `json:"unknowns,omitempty"`
	Gaps       []MigrationCompatibilityGap   `json:"gaps,omitempty"`
	NextCursor string                        `json:"next_cursor,omitempty"`
}
type MigrationObserveParams struct {
	Resource MigrationResource `json:"resource"`
}
type MigrationObserveResult struct {
	Found    bool               `json:"found"`
	Resource *MigrationResource `json:"resource,omitempty"`
}
type MigrationStep struct {
	OperationID string   `json:"operation_id"`
	ResourceID  string   `json:"resource_id"`
	Capability  string   `json:"capability"`
	Version     string   `json:"version,omitempty"`
	Action      string   `json:"action"`
	DependsOn   []string `json:"depends_on,omitempty"`
}
type MigrationApplyParams struct {
	Step     MigrationStep     `json:"step"`
	Resource MigrationResource `json:"resource"`
}
type MigrationApplyResult struct {
	Accepted bool `json:"accepted"`
}

type MigrationArtifact struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
	Format string `json:"format"`
	Data   []byte `json:"data"`
}
type MigrationExportParams struct {
	Resource MigrationResource `json:"resource"`
}
type MigrationExportResult struct {
	Artifact MigrationArtifact `json:"artifact"`
}
type MigrationImportParams struct {
	Resource MigrationResource `json:"resource"`
	Artifact MigrationArtifact `json:"artifact"`
}
type MigrationImportResult struct {
	Accepted bool `json:"accepted"`
}
type MigrationArtifactObserveParams struct {
	Resource MigrationResource `json:"resource"`
}
type MigrationArtifactObserveResult struct {
	Found         bool               `json:"found"`
	Resource      *MigrationResource `json:"resource,omitempty"`
	NativeBinding string             `json:"native_binding,omitempty"`
	Attestation   []byte             `json:"attestation,omitempty"`
}

type MigrationExtension interface {
	Discover(context.Context, MigrationDiscoverParams) (MigrationDiscoverResult, error)
	Observe(context.Context, MigrationObserveParams) (MigrationObserveResult, error)
	Apply(context.Context, MigrationApplyParams) (MigrationApplyResult, error)
}

// RegisterMigration registers only non-nil handlers, allowing discovery-only
// and apply-only plugins without pretending unavailable capabilities exist.
func RegisterMigration(server *Server, discover func(context.Context, MigrationDiscoverParams) (MigrationDiscoverResult, error), observe func(context.Context, MigrationObserveParams) (MigrationObserveResult, error), apply func(context.Context, MigrationApplyParams) (MigrationApplyResult, error)) {
	if server == nil {
		return
	}
	if discover != nil {
		server.Handle(CapabilityMigrationDiscover, migrationHandler(discover))
	}
	if observe != nil {
		server.Handle(CapabilityMigrationObserve, migrationHandler(observe))
	}
	if apply != nil {
		server.Handle(CapabilityMigrationApply, migrationHandler(apply))
	}
}

func RegisterMigrationArtifacts(server *Server, export func(context.Context, MigrationExportParams) (MigrationExportResult, error), importArtifact func(context.Context, MigrationImportParams) (MigrationImportResult, error), observe func(context.Context, MigrationArtifactObserveParams) (MigrationArtifactObserveResult, error)) {
	if server == nil {
		return
	}
	if export != nil {
		server.Handle(CapabilityMigrationExport, migrationHandler(export))
	}
	if importArtifact != nil {
		server.Handle(CapabilityMigrationImport, migrationHandler(importArtifact))
	}
	if observe != nil {
		server.Handle(CapabilityMigrationArtifactObserve, migrationHandler(observe))
	}
}

func migrationHandler[P, R any](fn func(context.Context, P) (R, error)) Handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params P
		if len(raw) == 0 {
			return nil, errors.New("migration plugin: parameters are required")
		}
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		return fn(ctx, params)
	}
}

type requestContextKey uint8

const (
	traceIDContextKey requestContextKey = iota
	operationIDContextKey
)

func TraceIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(traceIDContextKey).(string)
	return v
}
func OperationIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(operationIDContextKey).(string)
	return v
}
func contextWithRequestIDs(ctx context.Context, traceID, operationID string) context.Context {
	ctx = context.WithValue(ctx, traceIDContextKey, traceID)
	return context.WithValue(ctx, operationIDContextKey, operationID)
}

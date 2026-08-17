package plugin

// These host-owned wire mirrors remain byte-compatible with sdk/plugin. The
// internal tree must not import public adapters.
type migrationWireResource struct {
	ID           string                     `json:"id"`
	Kind         string                     `json:"kind"`
	Origin       migrationWireOrigin        `json:"origin"`
	Attributes   map[string]string          `json:"attributes,omitempty"`
	Requirements []migrationWireRequirement `json:"requirements,omitempty"`
}
type migrationWireOrigin struct {
	Source     string `json:"source"`
	NativeType string `json:"native_type"`
	NativeID   string `json:"native_id"`
}
type migrationWireRequirement struct {
	Capability string `json:"capability"`
	Version    string `json:"version,omitempty"`
}
type migrationWireEdge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation"`
	Required bool   `json:"required"`
}
type migrationWireUnknown struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Scope  string `json:"scope"`
	Reason string `json:"reason"`
}
type migrationWireGap struct {
	ResourceID       string `json:"resource_id"`
	Requirement      string `json:"requirement"`
	Compatibility    string `json:"compatibility"`
	Reason           string `json:"reason"`
	LostGuarantee    string `json:"lost_guarantee,omitempty"`
	RequiresApproval bool   `json:"requires_approval,omitempty"`
}
type migrationDiscoverParams struct {
	Cursor string `json:"cursor,omitempty"`
}
type migrationDiscoverResult struct {
	Status     string                  `json:"status"`
	Resources  []migrationWireResource `json:"resources,omitempty"`
	Edges      []migrationWireEdge     `json:"edges,omitempty"`
	Unknowns   []migrationWireUnknown  `json:"unknowns,omitempty"`
	Gaps       []migrationWireGap      `json:"gaps,omitempty"`
	NextCursor string                  `json:"next_cursor,omitempty"`
}
type migrationInspectParams struct {
	ResourceID string `json:"resource_id"`
}
type migrationInspectResult struct {
	Found    bool                   `json:"found"`
	Resource *migrationWireResource `json:"resource,omitempty"`
}
type migrationObserveParams struct {
	Resource migrationWireResource `json:"resource"`
}
type migrationObserveResult struct {
	Found    bool                   `json:"found"`
	Resource *migrationWireResource `json:"resource,omitempty"`
}
type migrationWireStep struct {
	OperationID string   `json:"operation_id"`
	ResourceID  string   `json:"resource_id"`
	Capability  string   `json:"capability"`
	Version     string   `json:"version,omitempty"`
	Action      string   `json:"action"`
	DependsOn   []string `json:"depends_on,omitempty"`
}
type migrationApplyParams struct {
	Step     migrationWireStep     `json:"step"`
	Resource migrationWireResource `json:"resource"`
}
type migrationApplyResult struct {
	Accepted bool `json:"accepted"`
}
type migrationWireArtifact struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
	Format string `json:"format"`
	Data   []byte `json:"data"`
}
type migrationExportParams struct {
	Resource migrationWireResource `json:"resource"`
}
type migrationExportResult struct {
	Artifact migrationWireArtifact `json:"artifact"`
}
type migrationImportParams struct {
	Resource migrationWireResource `json:"resource"`
	Artifact migrationWireArtifact `json:"artifact"`
}
type migrationImportResult struct {
	Accepted bool `json:"accepted"`
}
type migrationArtifactObserveParams struct {
	Resource migrationWireResource `json:"resource"`
}
type migrationArtifactObserveResult struct {
	Found         bool                   `json:"found"`
	Resource      *migrationWireResource `json:"resource,omitempty"`
	NativeBinding string                 `json:"native_binding,omitempty"`
	Attestation   []byte                 `json:"attestation,omitempty"`
}

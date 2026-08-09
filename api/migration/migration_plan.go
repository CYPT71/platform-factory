package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

const (
	MaxDependencyEdges = 200000
	MaxMigrationSteps  = 200000
	MaxUnknownBytes    = 1 << 20
)

// MigrationPlan represents a complete migration action plan
type MigrationPlan struct {
	// Version indicates the format version of this migration plan
	Version string `json:"version" yaml:"version"`

	// Resources contains all discovered resources involved in the migration
	Resources []Resource `json:"resources,omitempty" yaml:"resources,omitempty"`

	// Graph represents the dependency relationships between resources
	Graph []DependencyEdge `json:"graph,omitempty" yaml:"graph,omitempty"`

	// Steps defines the ordered sequence of operations needed for migration
	Steps []MigrationStep `json:"steps,omitempty" yaml:"steps,omitempty"`

	// Gaps captures compatibility issues that might prevent a successful migration
	Gaps []CompatibilityGap `json:"gaps,omitempty" yaml:"gaps,omitempty"`

	// Unknowns records observations that couldn't be interpreted due to incomplete or unrecognized data
	Unknowns []UnknownObservation `json:"unknowns,omitempty" yaml:"unknowns,omitempty"`

	// DiscoveryStatus indicates the overall status of discovery operation
	DiscoveryStatus DiscoveryStatus `json:"discovery_status,omitempty" yaml:"discovery_status,omitempty"`

	// Digest is the canonical hash of this migration plan for verification and comparison
	Digest string `json:"digest" yaml:"digest"`
}

// MigrationStep represents a single operation in the migration process
type MigrationStep struct {
	// OperationID uniquely identifies this step
	OperationID string `json:"operation_id" yaml:"operation_id"`

	// ResourceID identifies which resource this step operates on
	ResourceID ResourceID `json:"resource_id" yaml:"resource_id"`

	// Capability describes what operation will be performed (discover, apply, etc.)
	Capability string `json:"capability" yaml:"capability"`

	// Action indicates the type of operation (create, update, delete)
	Action string `json:"action" yaml:"action"`

	// Status describes the current state of this step
	Status string `json:"status" yaml:"status"`
}

// CompatibilityGap records a loss against the canonical resource requirements.
// It intentionally carries no target implementation identity: resolution belongs
// to the host and happens after the canonical plan is built.
type CompatibilityGap struct {
	ResourceID       string        `json:"resource_id" yaml:"resource_id"`
	Requirement      string        `json:"requirement" yaml:"requirement"`
	Status           Compatibility `json:"status" yaml:"status"`
	Reason           string        `json:"reason" yaml:"reason"`
	LostGuarantee    string        `json:"lost_guarantee,omitempty" yaml:"lost_guarantee,omitempty"`
	RequiresApproval bool          `json:"requires_approval" yaml:"requires_approval"`
}

// UnknownObservation records an observation that couldn't be interpreted
type UnknownObservation struct {
	// SourcePlugin identifies which plugin generated this observation
	SourcePlugin string `json:"source_plugin" yaml:"source_plugin"`

	// ObservationType describes the type of observation
	ObservationType string `json:"observation_type" yaml:"observation_type"`

	Scope string `json:"scope" yaml:"scope"`

	Reason string `json:"reason" yaml:"reason"`
}

// Validate checks that a MigrationPlan has valid structure and data
func (p *MigrationPlan) Validate() error {
	if err := p.validateContent(); err != nil {
		return err
	}
	if p.Digest == "" {
		return &ValidationError{"migration plan digest cannot be empty"}
	}
	want, err := p.ComputeDigest()
	if err != nil {
		return err
	}
	if p.Digest != want {
		return &ValidationError{"migration plan digest does not match canonical content"}
	}
	return nil
}

func (p *MigrationPlan) validateContent() error {
	if p.Version != FormatVersion {
		return &ValidationError{fmt.Sprintf("unsupported migration plan version %q", p.Version)}
	}
	if !p.DiscoveryStatus.valid() {
		return &ValidationError{"invalid discovery status"}
	}
	if len(p.Resources) > MaxResources || len(p.Graph) > MaxDependencyEdges || len(p.Steps) > MaxMigrationSteps {
		return &ValidationError{"migration plan exceeds collection limit"}
	}

	resources := make(map[string]struct{}, len(p.Resources))
	origins := make(map[string]struct{}, len(p.Resources))
	for _, res := range p.Resources {
		if err := res.Validate(); err != nil {
			return fmt.Errorf("invalid resource in migration plan: %w", err)
		}
		if _, ok := resources[res.ID]; ok {
			return duplicateError("resource ID", res.ID)
		}
		resources[res.ID] = struct{}{}
		key := resourceKey(ResourceID{PluginID: res.Source.PluginID, NativeType: res.Source.NativeType, NativeID: res.Source.NativeID})
		if _, ok := origins[key]; ok {
			return duplicateError("resource origin", key)
		}
		origins[key] = struct{}{}
	}

	// Validate each dependency edge
	edges := make(map[string]struct{}, len(p.Graph))
	for _, edge := range p.Graph {
		if err := edge.Validate(); err != nil {
			return fmt.Errorf("invalid dependency edge in migration plan: %w", err)
		}
		if _, ok := origins[resourceKey(edge.From)]; !ok {
			return &ValidationError{"dependency edge references unknown from resource"}
		}
		if _, ok := origins[resourceKey(edge.To)]; !ok {
			return &ValidationError{"dependency edge references unknown to resource"}
		}
		key := resourceKey(edge.From) + "\x00" + resourceKey(edge.To) + "\x00" + edge.Relation
		if _, ok := edges[key]; ok {
			return duplicateError("dependency edge", key)
		}
		edges[key] = struct{}{}
	}

	// Validate each step
	operations := make(map[string]struct{}, len(p.Steps))
	for _, step := range p.Steps {
		if invalidText(step.OperationID) {
			return &ValidationError{"migration step operation ID cannot be empty"}
		}
		if _, ok := operations[step.OperationID]; ok {
			return duplicateError("operation ID", step.OperationID)
		}
		operations[step.OperationID] = struct{}{}
		if err := step.ResourceID.Validate(); err != nil {
			return fmt.Errorf("invalid migration step resource: %w", err)
		}
		if _, ok := origins[resourceKey(step.ResourceID)]; !ok {
			return &ValidationError{"migration step references unknown resource"}
		}
		if invalidText(step.Capability) || invalidText(step.Action) || invalidText(step.Status) {
			return &ValidationError{"migration step fields cannot be empty"}
		}
	}
	for _, gap := range p.Gaps {
		if !gap.Status.valid() || invalidText(gap.ResourceID) || invalidText(gap.Requirement) || invalidText(gap.Reason) {
			return &ValidationError{"invalid compatibility gap"}
		}
		if _, ok := resources[gap.ResourceID]; !ok {
			return &ValidationError{"compatibility gap references unknown resource"}
		}
	}
	for _, unknown := range p.Unknowns {
		if invalidText(unknown.SourcePlugin) || invalidText(unknown.ObservationType) || invalidText(unknown.Scope) || invalidText(unknown.Reason) {
			return &ValidationError{"invalid unknown observation"}
		}
	}
	return nil
}

func resourceKey(id ResourceID) string {
	return id.PluginID + "\x00" + id.NativeType + "\x00" + id.NativeID
}

// Canonical returns a deeply independent, stably ordered representation.
func (p MigrationPlan) Canonical() MigrationPlan {
	p.Digest = ""
	p.Resources = append([]Resource(nil), p.Resources...)
	for i := range p.Resources {
		p.Resources[i].Requirements = append([]Requirement(nil), p.Resources[i].Requirements...)
		sort.Slice(p.Resources[i].Requirements, func(a, b int) bool {
			x, y := p.Resources[i].Requirements[a], p.Resources[i].Requirements[b]
			return x.Capability+"\x00"+x.Version < y.Capability+"\x00"+y.Version
		})
	}
	sort.Slice(p.Resources, func(i, j int) bool { return p.Resources[i].ID < p.Resources[j].ID })
	p.Graph = append([]DependencyEdge(nil), p.Graph...)
	sort.Slice(p.Graph, func(i, j int) bool {
		a, b := p.Graph[i], p.Graph[j]
		return fmt.Sprintf("%s\x00%s\x00%s\x00%t", resourceKey(a.From), resourceKey(a.To), a.Relation, a.Required) <
			fmt.Sprintf("%s\x00%s\x00%s\x00%t", resourceKey(b.From), resourceKey(b.To), b.Relation, b.Required)
	})
	p.Steps = append([]MigrationStep(nil), p.Steps...)
	sort.Slice(p.Steps, func(i, j int) bool { return p.Steps[i].OperationID < p.Steps[j].OperationID })
	p.Gaps = append([]CompatibilityGap(nil), p.Gaps...)
	sort.Slice(p.Gaps, func(i, j int) bool {
		a, b := p.Gaps[i], p.Gaps[j]
		return a.ResourceID+"\x00"+a.Requirement+"\x00"+string(a.Status)+"\x00"+a.Reason+"\x00"+a.LostGuarantee+fmt.Sprint(a.RequiresApproval) <
			b.ResourceID+"\x00"+b.Requirement+"\x00"+string(b.Status)+"\x00"+b.Reason+"\x00"+b.LostGuarantee+fmt.Sprint(b.RequiresApproval)
	})
	p.Unknowns = append([]UnknownObservation(nil), p.Unknowns...)
	sort.Slice(p.Unknowns, func(i, j int) bool {
		a, b := p.Unknowns[i], p.Unknowns[j]
		ak := a.SourcePlugin + "\x00" + a.ObservationType + "\x00" + a.Scope + "\x00" + a.Reason
		bk := b.SourcePlugin + "\x00" + b.ObservationType + "\x00" + b.Scope + "\x00" + b.Reason
		return ak < bk
	})
	return p
}

func (p MigrationPlan) ComputeDigest() (string, error) {
	canonical := p.Canonical()
	if err := canonical.validateContent(); err != nil {
		return "", err
	}
	b, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal canonical migration plan: %w", err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (p *MigrationPlan) SetDigest() error {
	digest, err := p.ComputeDigest()
	if err == nil {
		p.Digest = digest
	}
	return err
}

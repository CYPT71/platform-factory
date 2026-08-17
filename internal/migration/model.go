// Package migration owns the private migration domain model and invariants.
// Its types are intentionally distinct from api/migration transport DTOs: the
// latter preserve a stable wire representation, while this package models host
// decisions. Adapters belong at an outer boundary and never point back to API.
package migration

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	MaxResources    = 100000
	MaxEdges        = 200000
	MaxRequirements = 1024
	MaxUnknowns     = 100000
	MaxGaps         = 100000
	MaxAttributes   = 1024
)

type DiscoveryStatus string

const (
	DiscoveryComplete DiscoveryStatus = "complete"
	DiscoveryPartial  DiscoveryStatus = "partial"
	DiscoveryFailed   DiscoveryStatus = "failed"
)

type Compatibility string

const (
	CompatibilityDirect      Compatibility = "direct"
	CompatibilityAdaptable   Compatibility = "adaptable"
	CompatibilityDegraded    Compatibility = "degraded"
	CompatibilityUnsupported Compatibility = "unsupported"
)

type ResourceOrigin struct {
	Source     string
	NativeType string
	NativeID   string
}

type Requirement struct {
	Capability string
	Version    string
}

type Resource struct {
	ID           string
	Kind         string
	Origin       ResourceOrigin
	Attributes   map[string]string
	Requirements []Requirement
}

// DependencyEdge means From depends on To. The planner therefore schedules To
// before From. Both required and optional edges participate in cycle checks:
// accepting a cyclic canonical graph would make ordering ambiguous.
type DependencyEdge struct {
	From     string
	To       string
	Relation string
	Required bool
}

type UnknownObservation struct {
	Source string
	Kind   string
	Scope  string
	Reason string
}

type CompatibilityGap struct {
	ResourceID       string
	Requirement      string
	Compatibility    Compatibility
	Reason           string
	LostGuarantee    string
	RequiresApproval bool
}

type ExternalDependency struct {
	ResourceID string
	Kind       string
	Reference  string
	Required   bool
}

type Transformation struct {
	ResourceID string
	Field      string
	From       string
	To         string
	Reason     string
}

// Aggregate is the normalized, implementation-independent input to planning.
type Aggregate struct {
	Discovery            DiscoveryStatus
	Resources            []Resource
	Edges                []DependencyEdge
	Unknowns             []UnknownObservation
	Gaps                 []CompatibilityGap
	ExternalDependencies []ExternalDependency
	Transformations      []Transformation
}

// ComputeDigest identifies the canonical logical discovery result. Provider
// order and map iteration cannot influence it.
func (a Aggregate) ComputeDigest() (string, error) {
	if err := a.Validate(); err != nil {
		return "", err
	}
	return digestValue("platform-factory/migration-input/v1\x00", a.Canonical())
}

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return "migration domain validation: " + e.Message }

func IsValidationError(err error) bool {
	var target *ValidationError
	return errors.As(err, &target)
}

func (a Aggregate) Validate() error {
	if !validDiscovery(a.Discovery) {
		return invalid("invalid discovery status")
	}
	if a.Discovery == DiscoveryPartial && len(a.Unknowns) == 0 {
		return invalid("partial discovery must preserve at least one unknown observation")
	}
	if len(a.Resources) > MaxResources || len(a.Edges) > MaxEdges || len(a.Unknowns) > MaxUnknowns || len(a.Gaps) > MaxGaps {
		return invalid("aggregate exceeds collection limit")
	}
	ids := make(map[string]struct{}, len(a.Resources))
	origins := make(map[string]struct{}, len(a.Resources))
	for _, resource := range a.Resources {
		if err := validateResource(resource); err != nil {
			return err
		}
		if _, exists := ids[resource.ID]; exists {
			return invalid("duplicate resource ID %q", resource.ID)
		}
		ids[resource.ID] = struct{}{}
		origin := resource.Origin.Source + "\x00" + resource.Origin.NativeType + "\x00" + resource.Origin.NativeID
		if _, exists := origins[origin]; exists {
			return invalid("duplicate resource origin")
		}
		origins[origin] = struct{}{}
	}
	edges := make(map[string]struct{}, len(a.Edges))
	for _, edge := range a.Edges {
		if invalidText(edge.From) || invalidText(edge.To) || invalidText(edge.Relation) {
			return invalid("dependency edge contains an empty or invalid field")
		}
		if edge.From == edge.To {
			return invalid("resource %q depends on itself", edge.From)
		}
		if _, exists := ids[edge.From]; !exists {
			return invalid("dependency edge references unknown resource %q", edge.From)
		}
		if _, exists := ids[edge.To]; !exists {
			return invalid("dependency edge references unknown resource %q", edge.To)
		}
		key := edge.From + "\x00" + edge.To + "\x00" + edge.Relation + fmt.Sprint(edge.Required)
		if _, exists := edges[key]; exists {
			return invalid("duplicate dependency edge")
		}
		edges[key] = struct{}{}
	}
	unknowns := make(map[string]struct{}, len(a.Unknowns))
	for _, unknown := range a.Unknowns {
		if invalidText(unknown.Source) || invalidText(unknown.Kind) || invalidText(unknown.Scope) || invalidText(unknown.Reason) {
			return invalid("unknown observation contains an empty or invalid field")
		}
		key := unknownKey(unknown)
		if _, exists := unknowns[key]; exists {
			return invalid("duplicate unknown observation")
		}
		unknowns[key] = struct{}{}
	}
	gaps := make(map[string]struct{}, len(a.Gaps))
	for _, gap := range a.Gaps {
		if _, exists := ids[gap.ResourceID]; !exists {
			return invalid("compatibility gap references unknown resource %q", gap.ResourceID)
		}
		if invalidText(gap.Requirement) || invalidText(gap.Reason) || !validCompatibility(gap.Compatibility) {
			return invalid("invalid compatibility gap")
		}
		if invalidOptionalText(gap.LostGuarantee) {
			return invalid("compatibility gap contains an invalid lost guarantee")
		}
		if !resourceHasRequirement(a.Resources, gap.ResourceID, gap.Requirement) {
			return invalid("compatibility gap references unknown requirement %q on resource %q", gap.Requirement, gap.ResourceID)
		}
		key := gapKey(gap)
		if _, exists := gaps[key]; exists {
			return invalid("duplicate compatibility gap")
		}
		gaps[key] = struct{}{}
	}
	externals := map[string]struct{}{}
	for _, dependency := range a.ExternalDependencies {
		if _, ok := ids[dependency.ResourceID]; !ok || invalidText(dependency.Kind) || invalidText(dependency.Reference) || secretValue(dependency.Reference) {
			return invalid("invalid external dependency")
		}
		key := externalDependencyKey(dependency)
		if _, ok := externals[key]; ok {
			return invalid("duplicate external dependency")
		}
		externals[key] = struct{}{}
	}
	transformations := map[string]struct{}{}
	for _, transformation := range a.Transformations {
		if _, ok := ids[transformation.ResourceID]; !ok || invalidText(transformation.Field) || invalidOptionalText(transformation.From) || invalidOptionalText(transformation.To) || invalidText(transformation.Reason) || secretKey(transformation.Field) || secretValue(transformation.From) || secretValue(transformation.To) {
			return invalid("invalid or secret-like transformation")
		}
		key := transformationKey(transformation)
		if _, ok := transformations[key]; ok {
			return invalid("duplicate transformation")
		}
		transformations[key] = struct{}{}
	}
	if _, err := topologicalResourceIDs(a.Resources, a.Edges); err != nil {
		return err
	}
	return nil
}

func resourceHasRequirement(resources []Resource, resourceID, name string) bool {
	for _, resource := range resources {
		if resource.ID != resourceID {
			continue
		}
		for _, requirement := range resource.Requirements {
			if name == requirement.Capability || name == requirement.Capability+"@"+requirement.Version {
				return true
			}
		}
	}
	return false
}

func validateResource(resource Resource) error {
	if invalidText(resource.ID) || invalidText(resource.Kind) || invalidText(resource.Origin.Source) ||
		invalidText(resource.Origin.NativeType) || invalidText(resource.Origin.NativeID) {
		return invalid("resource contains an empty or invalid identity field")
	}
	if len(resource.Requirements) > MaxRequirements {
		return invalid("resource %q has too many requirements", resource.ID)
	}
	requirements := make(map[string]struct{}, len(resource.Requirements))
	for _, requirement := range resource.Requirements {
		if invalidText(requirement.Capability) || invalidText(requirement.Version) {
			return invalid("resource %q has an invalid requirement", resource.ID)
		}
		key := requirement.Capability + "\x00" + requirement.Version
		if _, exists := requirements[key]; exists {
			return invalid("resource %q has a duplicate requirement", resource.ID)
		}
		requirements[key] = struct{}{}
	}
	if len(resource.Attributes) > MaxAttributes {
		return invalid("resource %q has too many attributes", resource.ID)
	}
	for key, value := range resource.Attributes {
		if invalidText(key) || secretKey(key) || invalidOptionalText(value) || secretValue(value) {
			return invalid("resource %q has a secret-like key or invalid attribute", resource.ID)
		}
	}
	return nil
}

func (a Aggregate) Canonical() Aggregate {
	out := a
	out.Resources = append([]Resource(nil), a.Resources...)
	for i := range out.Resources {
		out.Resources[i].Attributes = cloneMap(out.Resources[i].Attributes)
		out.Resources[i].Requirements = append([]Requirement(nil), out.Resources[i].Requirements...)
		sort.Slice(out.Resources[i].Requirements, func(x, y int) bool {
			a, b := out.Resources[i].Requirements[x], out.Resources[i].Requirements[y]
			return a.Capability+"\x00"+a.Version < b.Capability+"\x00"+b.Version
		})
	}
	sort.Slice(out.Resources, func(i, j int) bool { return out.Resources[i].ID < out.Resources[j].ID })
	out.Edges = append([]DependencyEdge(nil), a.Edges...)
	sort.Slice(out.Edges, func(i, j int) bool { return edgeKey(out.Edges[i]) < edgeKey(out.Edges[j]) })
	out.Unknowns = append([]UnknownObservation(nil), a.Unknowns...)
	sort.Slice(out.Unknowns, func(i, j int) bool { return unknownKey(out.Unknowns[i]) < unknownKey(out.Unknowns[j]) })
	out.Gaps = append([]CompatibilityGap(nil), a.Gaps...)
	sort.Slice(out.Gaps, func(i, j int) bool { return gapKey(out.Gaps[i]) < gapKey(out.Gaps[j]) })
	out.ExternalDependencies = append([]ExternalDependency(nil), a.ExternalDependencies...)
	sort.Slice(out.ExternalDependencies, func(i, j int) bool {
		return externalDependencyKey(out.ExternalDependencies[i]) < externalDependencyKey(out.ExternalDependencies[j])
	})
	out.Transformations = append([]Transformation(nil), a.Transformations...)
	sort.Slice(out.Transformations, func(i, j int) bool {
		return transformationKey(out.Transformations[i]) < transformationKey(out.Transformations[j])
	})
	return out
}

func validDiscovery(status DiscoveryStatus) bool {
	return status == DiscoveryComplete || status == DiscoveryPartial || status == DiscoveryFailed
}

func validCompatibility(value Compatibility) bool {
	return value == CompatibilityDirect || value == CompatibilityAdaptable || value == CompatibilityDegraded || value == CompatibilityUnsupported
}

func invalidText(value string) bool {
	return strings.TrimSpace(value) == "" || strings.IndexByte(value, 0) >= 0 || len(value) > 1024
}

func invalidOptionalText(value string) bool {
	return strings.IndexByte(value, 0) >= 0 || len(value) > 1024
}

func secretKey(value string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "_", ".", "_").Replace(value))
	for _, marker := range []string{"password", "passwd", "secret", "private_key", "access_token", "api_key", "credential"} {
		if normalized == marker || strings.HasSuffix(normalized, "_"+marker) {
			return true
		}
	}
	return false
}

func secretValue(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	for _, marker := range []string{"secret-sentinel", "password=", "passwd=", "secret=", "access_token=", "api_key=", "private_key=", "-----begin private key", "-----begin rsa private key"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func cloneMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func edgeKey(e DependencyEdge) string {
	return e.From + "\x00" + e.To + "\x00" + e.Relation + fmt.Sprint(e.Required)
}
func unknownKey(u UnknownObservation) string {
	return u.Source + "\x00" + u.Kind + "\x00" + u.Scope + "\x00" + u.Reason
}
func gapKey(g CompatibilityGap) string {
	return g.ResourceID + "\x00" + g.Requirement + "\x00" + string(g.Compatibility) + "\x00" + g.Reason + "\x00" + g.LostGuarantee + fmt.Sprint(g.RequiresApproval)
}
func externalDependencyKey(d ExternalDependency) string {
	return d.ResourceID + "\x00" + d.Kind + "\x00" + d.Reference + fmt.Sprint(d.Required)
}
func transformationKey(t Transformation) string {
	return t.ResourceID + "\x00" + t.Field + "\x00" + t.From + "\x00" + t.To + "\x00" + t.Reason
}
func invalid(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

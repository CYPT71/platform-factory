package plugin

import (
	"context"
	"errors"
	"fmt"

	appmigration "github.com/CYPT71/secure-oci-base/internal/app/migration"
	domainmigration "github.com/CYPT71/secure-oci-base/internal/migration"
)

const (
	migrationDiscoverCapability = "migration.discover"
	migrationObserveCapability  = "migration.observe"
	migrationApplyCapability    = "migration.apply"
)

func (r *Registry) verifiedClient(id, digest, capability string) (*Client, error) {
	if r == nil {
		return nil, errors.New("plugin registry is required")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	state, ok := r.states[id]
	if !ok || !r.availableLocked(id) {
		return nil, fmt.Errorf("plugin %q is not available", id)
	}
	if state.verifiedManifest.Digest != digest {
		return nil, fmt.Errorf("plugin %q verified digest does not match resolution", id)
	}
	if !state.verifiedManifest.HasCapability(capability) || !state.client.HasCapability(capability) {
		return nil, fmt.Errorf("plugin %q does not have live verified capability %q", id, capability)
	}
	return state.client, nil
}

type MigrationDiscoverySource struct {
	id, digest string
	registry   *Registry
}

func NewMigrationDiscoverySource(registry *Registry, id, digest string) *MigrationDiscoverySource {
	return &MigrationDiscoverySource{registry: registry, id: id, digest: digest}
}
func (s *MigrationDiscoverySource) SourceID() string { return s.id }
func (s *MigrationDiscoverySource) DiscoverPage(ctx context.Context, cursor string) (appmigration.DiscoveryPage, error) {
	client, err := s.registry.verifiedClient(s.id, s.digest, migrationDiscoverCapability)
	if err != nil {
		return appmigration.DiscoveryPage{}, err
	}
	var wire migrationDiscoverResult
	if err := client.Call(ctx, "v1."+migrationDiscoverCapability, migrationDiscoverParams{Cursor: cursor}, &wire); err != nil {
		return appmigration.DiscoveryPage{}, err
	}
	page := discoveryPageFromWire(wire)
	for i := range page.Resources {
		if page.Resources[i].Origin.Source == "" {
			page.Resources[i].Origin.Source = s.id
		}
		if page.Resources[i].Origin.Source != s.id {
			return appmigration.DiscoveryPage{}, errors.New("migration discover: resource source does not match verified plugin identity")
		}
	}
	for i := range page.Unknowns {
		if page.Unknowns[i].Source == "" {
			page.Unknowns[i].Source = s.id
		}
		if page.Unknowns[i].Source != s.id {
			return appmigration.DiscoveryPage{}, errors.New("migration discover: unknown source does not match verified plugin identity")
		}
	}
	return page, nil
}

// MigrationTargetFactory binds every operation to the exact identity and
// digest selected by the application resolver.
type MigrationTargetFactory struct{ Registry *Registry }

func (f MigrationTargetFactory) Open(ctx context.Context, resolved appmigration.ResolvedCapability) (appmigration.TargetOperations, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, err := f.Registry.verifiedClient(resolved.CandidateID, resolved.Digest, migrationApplyCapability)
	if err != nil {
		return nil, err
	}
	if !client.HasCapability(migrationObserveCapability) {
		return nil, errors.New("migration target lacks verified observe capability")
	}
	return &migrationTarget{registry: f.Registry, id: resolved.CandidateID, digest: resolved.Digest}, nil
}

type migrationTarget struct {
	registry   *Registry
	id, digest string
}
type normalizedObservation struct {
	found    bool
	resource domainmigration.Resource
}

func (t *migrationTarget) Observe(ctx context.Context, desired domainmigration.Resource) (appmigration.TargetObservation, error) {
	client, err := t.registry.verifiedClient(t.id, t.digest, migrationObserveCapability)
	if err != nil {
		return appmigration.TargetObservation{}, err
	}
	var wire migrationObserveResult
	if err := client.Call(ctx, "v1."+migrationObserveCapability, migrationObserveParams{Resource: resourceToWire(desired)}, &wire); err != nil {
		return appmigration.TargetObservation{}, err
	}
	if !wire.Found {
		if wire.Resource != nil {
			return appmigration.TargetObservation{}, errors.New("migration observe: absent result included a resource")
		}
		return appmigration.TargetObservation{Native: normalizedObservation{}}, nil
	}
	if wire.Resource == nil {
		return appmigration.TargetObservation{}, errors.New("migration observe: found result omitted resource")
	}
	resource := resourceFromWire(*wire.Resource)
	if resource.Origin.Source == "" {
		resource.Origin.Source = desired.Origin.Source
	}
	if resource.Origin.Source != desired.Origin.Source {
		return appmigration.TargetObservation{}, errors.New("migration observe: resource source does not match desired canonical identity")
	}
	resource, err = canonicalMigrationResource(resource)
	if err != nil {
		return appmigration.TargetObservation{}, fmt.Errorf("migration observe: invalid normalized resource: %w", err)
	}
	return appmigration.TargetObservation{Native: normalizedObservation{found: true, resource: resource}}, nil
}
func (t *migrationTarget) Apply(ctx context.Context, step domainmigration.Step, resource domainmigration.Resource) error {
	client, err := t.registry.verifiedClient(t.id, t.digest, migrationApplyCapability)
	if err != nil {
		return err
	}
	var result migrationApplyResult
	err = client.CallWithIdempotency(ctx, step.OperationID, "v1."+migrationApplyCapability, migrationApplyParams{Step: stepToWire(step), Resource: resourceToWire(resource)}, &result)
	if err == nil && !result.Accepted {
		return errors.New("migration apply: plugin did not accept operation")
	}
	return err
}
func (t *migrationTarget) Verify(_ context.Context, desired domainmigration.Resource, observation appmigration.TargetObservation) (bool, error) {
	normalized, ok := observation.Native.(normalizedObservation)
	if !ok {
		return false, errors.New("migration verify: invalid host observation")
	}
	if !normalized.found {
		return false, nil
	}
	desired, err := canonicalMigrationResource(desired)
	if err != nil {
		return false, fmt.Errorf("migration verify: invalid desired resource: %w", err)
	}
	observed, err := canonicalMigrationResource(normalized.resource)
	if err != nil {
		return false, fmt.Errorf("migration verify: invalid observed resource: %w", err)
	}
	difference := resourceDistance(desired, observed)
	return difference == 0, nil
}

func resourceToWire(r domainmigration.Resource) migrationWireResource {
	w := migrationWireResource{ID: r.ID, Kind: r.Kind, Origin: migrationWireOrigin{Source: r.Origin.Source, NativeType: r.Origin.NativeType, NativeID: r.Origin.NativeID}}
	if len(r.Attributes) > 0 {
		w.Attributes = make(map[string]string, len(r.Attributes))
	}
	for k, v := range r.Attributes {
		w.Attributes[k] = v
	}
	for _, q := range r.Requirements {
		w.Requirements = append(w.Requirements, migrationWireRequirement{q.Capability, q.Version})
	}
	return w
}
func resourceFromWire(w migrationWireResource) domainmigration.Resource {
	r := domainmigration.Resource{ID: w.ID, Kind: w.Kind, Origin: domainmigration.ResourceOrigin{Source: w.Origin.Source, NativeType: w.Origin.NativeType, NativeID: w.Origin.NativeID}}
	if len(w.Attributes) > 0 {
		r.Attributes = make(map[string]string, len(w.Attributes))
	}
	for k, v := range w.Attributes {
		r.Attributes[k] = v
	}
	for _, q := range w.Requirements {
		r.Requirements = append(r.Requirements, domainmigration.Requirement{Capability: q.Capability, Version: q.Version})
	}
	return r
}
func stepToWire(s domainmigration.Step) migrationWireStep {
	w := migrationWireStep{OperationID: string(s.OperationID), ResourceID: s.ResourceID, Capability: s.Capability, Version: s.Version, Action: s.Action}
	for _, d := range s.DependsOn {
		w.DependsOn = append(w.DependsOn, string(d))
	}
	return w
}
func discoveryPageFromWire(w migrationDiscoverResult) appmigration.DiscoveryPage {
	p := appmigration.DiscoveryPage{Status: domainmigration.DiscoveryStatus(w.Status), NextCursor: w.NextCursor}
	for _, r := range w.Resources {
		p.Resources = append(p.Resources, resourceFromWire(r))
	}
	for _, e := range w.Edges {
		p.Edges = append(p.Edges, domainmigration.DependencyEdge{From: e.From, To: e.To, Relation: e.Relation, Required: e.Required})
	}
	for _, u := range w.Unknowns {
		p.Unknowns = append(p.Unknowns, domainmigration.UnknownObservation{Source: u.Source, Kind: u.Kind, Scope: u.Scope, Reason: u.Reason})
	}
	for _, g := range w.Gaps {
		p.Gaps = append(p.Gaps, domainmigration.CompatibilityGap{ResourceID: g.ResourceID, Requirement: g.Requirement, Compatibility: domainmigration.Compatibility(g.Compatibility), Reason: g.Reason, LostGuarantee: g.LostGuarantee, RequiresApproval: g.RequiresApproval})
	}
	return p
}

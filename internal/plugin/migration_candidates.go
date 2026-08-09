package plugin

import (
	"context"
	"sort"

	appmigration "github.com/CYPT71/secure-oci-base/internal/app/migration"
)

// Candidates adapts the existing registry to the migration application's
// inward-facing capability port. Declarations remain visible but unavailable.
func (r *Registry) Candidates(ctx context.Context, requirement appmigration.CapabilityRequirement) ([]appmigration.CapabilityCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := append([]string(nil), r.byCap[requirement.Capability]...)
	sort.Strings(names)
	result := make([]appmigration.CapabilityCandidate, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		manifest, ok := r.plugins[name]
		if !ok {
			continue
		}
		state := r.states[name]
		if state.verified {
			manifest = cloneManifest(state.verifiedManifest)
		}
		// Manifest permissions are requests, not evidence that the sandbox
		// actually granted them. Until effective permissions are observed and
		// recorded, expose none so resolution fails closed for privileged work.
		network, filesystem, secrets := []string(nil), []string(nil), []string(nil)
		sort.Strings(network)
		sort.Strings(filesystem)
		sort.Strings(secrets)
		result = append(result, appmigration.CapabilityCandidate{
			ID: name, Capability: requirement.Capability, Version: manifest.Version, Digest: manifest.Digest,
			Platforms:   append([]string(nil), manifest.Platforms...),
			Permissions: appmigration.CapabilityPermissions{Network: network, Filesystem: filesystem, Secrets: secrets},
			Evidence: appmigration.CapabilityEvidence{
				Declared: state.declared, Discovered: state.discovered,
				Negotiated: state.negotiated, Verified: state.verified,
				Available: state.declared && state.discovered && state.negotiated && state.verified && state.client.isAlive(),
			}, Compatibility: appmigration.CompatibilityDirect,
		})
	}
	return result, nil
}

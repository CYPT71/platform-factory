package migration

import (
	"context"
	"testing"
)

type fuzzCandidates []CapabilityCandidate

func (c fuzzCandidates) Candidates(context.Context, CapabilityRequirement) ([]CapabilityCandidate, error) {
	return c, nil
}

func FuzzCapabilityResolutionEvidence(f *testing.F) {
	f.Add("direct", true, true, true, true, true)
	f.Add("degraded", true, true, false, true, true)
	f.Fuzz(func(t *testing.T, compatibility string, declared, discovered, negotiated, verified, available bool) {
		if len(compatibility) > 128 {
			t.Skip()
		}
		candidate := CapabilityCandidate{ID: "target", Digest: testDigest('f'), Capability: "migration.apply", Version: "v1", Compatibility: Compatibility(compatibility), Evidence: CapabilityEvidence{Declared: declared, Discovered: discovered, Negotiated: negotiated, Verified: verified, Available: available}}
		_, _ = NewCapabilityResolver(fuzzCandidates{candidate}).Resolve(context.Background(), CapabilityRequirement{Capability: "migration.apply", Version: "v1", AllowDegraded: true})
	})
}

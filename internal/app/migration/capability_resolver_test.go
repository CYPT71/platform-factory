package migration

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"testing"
)

type candidateSource struct {
	candidates []CapabilityCandidate
	err        error
}

type cancelingCandidateSource struct{ cancel context.CancelFunc }

func (s cancelingCandidateSource) Candidates(context.Context, CapabilityRequirement) ([]CapabilityCandidate, error) {
	s.cancel()
	return []CapabilityCandidate{available("candidate", CompatibilityDirect)}, nil
}

func (s candidateSource) Candidates(ctx context.Context, _ CapabilityRequirement) ([]CapabilityCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]CapabilityCandidate(nil), s.candidates...), s.err
}

func available(id string, compatibility Compatibility) CapabilityCandidate {
	return CapabilityCandidate{ID: id, Digest: testDigest('a'), Capability: "migration.apply", Version: "v1", Platforms: []string{"linux/amd64"}, Permissions: CapabilityPermissions{Filesystem: []string{"write"}}, Compatibility: compatibility,
		Evidence: CapabilityEvidence{Declared: true, Discovered: true, Negotiated: true, Verified: true, Available: true}}
}

func testDigest(digit byte) string { return "sha256:" + strings.Repeat(string(digit), 64) }

func TestCapabilityResolverFailClosed(t *testing.T) {
	tests := []struct {
		name        string
		candidates  []CapabilityCandidate
		requirement CapabilityRequirement
	}{
		{name: "zero"},
		{name: "declared unavailable", candidates: []CapabilityCandidate{{ID: "declared", Capability: "migration.apply", Evidence: CapabilityEvidence{Declared: true}}}},
		{name: "untrusted", candidates: []CapabilityCandidate{{ID: "untrusted", Capability: "migration.apply", Evidence: CapabilityEvidence{Declared: true, Discovered: true, Negotiated: true, Available: true}}}},
		{name: "verified digest absent", candidates: []CapabilityCandidate{func() CapabilityCandidate {
			candidate := available("absent", CompatibilityDirect)
			candidate.Digest = ""
			return candidate
		}()}},
		{name: "verified digest malformed", candidates: []CapabilityCandidate{func() CapabilityCandidate {
			candidate := available("malformed", CompatibilityDirect)
			candidate.Digest = "sha256:not-a-digest"
			return candidate
		}()}},
		{name: "platform", candidates: []CapabilityCandidate{available("wrong-platform", CompatibilityDirect)}, requirement: CapabilityRequirement{Platforms: []string{"linux/arm64"}}},
		{name: "permissions", candidates: []CapabilityCandidate{available("underprivileged", CompatibilityDirect)}, requirement: CapabilityRequirement{Permissions: CapabilityPermissions{Filesystem: []string{"admin"}}}},
		{name: "degraded denied", candidates: []CapabilityCandidate{available("degraded", CompatibilityDegraded)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requirement := tt.requirement
			requirement.Capability, requirement.Version = "migration.apply", "v1"
			_, err := NewCapabilityResolver(candidateSource{candidates: tt.candidates}).Resolve(context.Background(), requirement)
			if err == nil {
				t.Fatal("expected resolution to fail closed")
			}
		})
	}
}

func TestCapabilityResolverSelectsDeterministically(t *testing.T) {
	base := []CapabilityCandidate{available("z-adaptable", CompatibilityAdaptable), available("b-direct", CompatibilityDirect), available("a-direct", CompatibilityDirect), available("d-degraded", CompatibilityDegraded)}
	requirement := CapabilityRequirement{Capability: "migration.apply", Version: "v1", Platforms: []string{"linux/amd64"}, Permissions: CapabilityPermissions{Filesystem: []string{"write"}}, AllowDegraded: true}
	for seed := int64(0); seed < 30; seed++ {
		candidates := append([]CapabilityCandidate(nil), base...)
		rand.New(rand.NewSource(seed)).Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
		resolved, err := NewCapabilityResolver(candidateSource{candidates: candidates}).Resolve(context.Background(), requirement)
		if err != nil {
			t.Fatal(err)
		}
		if resolved.CandidateID != "a-direct" || resolved.Compatibility != CompatibilityDirect {
			t.Fatalf("seed %d: got %+v", seed, resolved)
		}
	}
}

func TestCapabilityResolverStableTieBreak(t *testing.T) {
	a := available("z", CompatibilityDirect)
	a.Version, a.Digest = "v2", testDigest('b')
	b := available("a", CompatibilityDirect)
	b.Version, b.Digest = "v2", testDigest('a')
	c := available("newer", CompatibilityDirect)
	c.Version, c.Digest = "v3", testDigest('c')
	resolved, err := NewCapabilityResolver(candidateSource{candidates: []CapabilityCandidate{a, b, c}}).Resolve(context.Background(), CapabilityRequirement{Capability: "migration.apply"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.CandidateID != "newer" {
		t.Fatalf("version/digest/id tie-break selected %q", resolved.CandidateID)
	}
}

func TestCapabilityResolverAllowsDegradedExplicitly(t *testing.T) {
	resolved, err := NewCapabilityResolver(candidateSource{candidates: []CapabilityCandidate{available("degraded", CompatibilityDegraded)}}).Resolve(context.Background(), CapabilityRequirement{Capability: "migration.apply", Version: "v1", AllowDegraded: true})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Compatibility != CompatibilityDegraded {
		t.Fatalf("got %v", resolved.Compatibility)
	}
}

func TestCapabilityResolverCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewCapabilityResolver(candidateSource{}).Resolve(ctx, CapabilityRequirement{Capability: "migration.apply"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestCapabilityResolverRejectsInvalidInvocation(t *testing.T) {
	tests := []struct {
		name     string
		resolver *CapabilityResolver
		req      CapabilityRequirement
	}{
		{name: "nil resolver", resolver: nil, req: CapabilityRequirement{Capability: "migration.apply"}},
		{name: "nil candidates port", resolver: NewCapabilityResolver(nil), req: CapabilityRequirement{Capability: "migration.apply"}},
		{name: "empty capability", resolver: NewCapabilityResolver(candidateSource{})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.resolver.Resolve(context.Background(), tt.req); err == nil {
				t.Fatal("expected invalid invocation to fail")
			}
		})
	}
}

func TestCapabilityResolverPreservesCandidateSourceError(t *testing.T) {
	sentinel := errors.New("source unavailable")
	_, err := NewCapabilityResolver(candidateSource{err: sentinel}).Resolve(context.Background(), CapabilityRequirement{Capability: "migration.apply"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want wrapped source error", err)
	}
}

func TestCapabilityResolverObservesCancellationAfterCandidateLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, err := NewCapabilityResolver(cancelingCandidateSource{cancel: cancel}).Resolve(ctx, CapabilityRequirement{Capability: "migration.apply"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
}

func TestCapabilityResolverIgnoresMalformedAndWrongCapabilityCandidates(t *testing.T) {
	candidates := []CapabilityCandidate{
		available("", CompatibilityDirect),
		available("wrong", CompatibilityDirect),
		available("selected", CompatibilityDirect),
	}
	candidates[1].Capability = "migration.discover"
	resolved, err := NewCapabilityResolver(candidateSource{candidates: candidates}).Resolve(context.Background(), CapabilityRequirement{Capability: "migration.apply", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.CandidateID != "selected" {
		t.Fatalf("selected %q", resolved.CandidateID)
	}
}

func TestCapabilityResolverAssessmentReasons(t *testing.T) {
	candidate := available("candidate", CompatibilityDirect)
	candidate.Version = "v2"
	_, err := NewCapabilityResolver(candidateSource{candidates: []CapabilityCandidate{candidate}}).Resolve(context.Background(), CapabilityRequirement{
		Capability: "migration.apply", Version: "v1", Platforms: []string{"linux/arm64"},
		Permissions: CapabilityPermissions{Network: []string{"egress"}, Filesystem: []string{"admin"}, Secrets: []string{"credential-ref"}},
	})
	if err == nil {
		t.Fatal("expected mismatched candidate to be rejected")
	}
}

func TestCapabilityResolverDefaultsCompatibilityAndCopiesReasons(t *testing.T) {
	candidate := available("candidate", "")
	candidate.Reasons = []string{"native mapping"}
	resolved, err := NewCapabilityResolver(candidateSource{candidates: []CapabilityCandidate{candidate}}).Resolve(context.Background(), CapabilityRequirement{Capability: "migration.apply", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Compatibility != CompatibilityDirect || len(resolved.Reasons) != 1 || resolved.Reasons[0] != "native mapping" {
		t.Fatalf("unexpected resolution: %+v", resolved)
	}
	candidate.Reasons[0] = "mutated"
	if resolved.Reasons[0] != "native mapping" {
		t.Fatal("resolved reasons alias candidate input")
	}
}

func TestCapabilityResolverRanksAdaptableAboveDegraded(t *testing.T) {
	resolved, err := NewCapabilityResolver(candidateSource{candidates: []CapabilityCandidate{
		available("degraded", CompatibilityDegraded), available("adaptable", CompatibilityAdaptable),
	}}).Resolve(context.Background(), CapabilityRequirement{Capability: "migration.apply", Version: "v1", AllowDegraded: true})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.CandidateID != "adaptable" {
		t.Fatalf("selected %q", resolved.CandidateID)
	}
}

func TestCapabilityResolverDigestThenIDTieBreak(t *testing.T) {
	a, b, c := available("z", CompatibilityDirect), available("a", CompatibilityDirect), available("b", CompatibilityDirect)
	a.Digest, b.Digest, c.Digest = testDigest('b'), testDigest('a'), testDigest('a')
	resolved, err := NewCapabilityResolver(candidateSource{candidates: []CapabilityCandidate{a, b, c}}).Resolve(context.Background(), CapabilityRequirement{Capability: "migration.apply", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.CandidateID != "a" {
		t.Fatalf("selected %q", resolved.CandidateID)
	}
}

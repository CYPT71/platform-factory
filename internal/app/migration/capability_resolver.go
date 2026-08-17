// Package migration contains application orchestration for migrations.
package migration

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	domainmigration "github.com/CYPT71/platform-factory/internal/migration"
)

// CapabilityCandidates is the inward-facing port used to inspect implementations.
// Implementations must return declared candidates too: the resolver keeps unavailable
// states explicit and never turns a declaration into proof of availability.
type CapabilityCandidates interface {
	Candidates(context.Context, CapabilityRequirement) ([]CapabilityCandidate, error)
}

// CapabilityRequirement is the application-level input needed for resolution.
type CapabilityRequirement struct {
	Capability    string
	Version       string
	Platforms     []string
	Permissions   CapabilityPermissions
	AllowDegraded bool
}

type CapabilityPermissions struct{ Network, Filesystem, Secrets []string }

// CapabilityEvidence records the distinct capability lifecycle observations.
type CapabilityEvidence struct {
	Declared   bool
	Discovered bool
	Negotiated bool
	Verified   bool
	Available  bool
}

// CapabilityCandidate is a narrow DTO; it intentionally exposes no plugin manifest.
type CapabilityCandidate struct {
	ID            string
	Capability    string
	Version       string
	Digest        string
	Platforms     []string
	Permissions   CapabilityPermissions
	Evidence      CapabilityEvidence
	Compatibility Compatibility
	Reasons       []string
}

// Compatibility describes how faithfully a candidate satisfies a requirement.
type Compatibility = domainmigration.Compatibility

const (
	CompatibilityUnsupported = domainmigration.CompatibilityUnsupported
	CompatibilityDegraded    = domainmigration.CompatibilityDegraded
	CompatibilityAdaptable   = domainmigration.CompatibilityAdaptable
	CompatibilityDirect      = domainmigration.CompatibilityDirect
)

// ResolvedCapability is the deterministic, explainable resolution result.
type ResolvedCapability struct {
	CandidateID   string
	Digest        string
	Compatibility Compatibility
	Reasons       []string
}

// CapabilityResolver selects only demonstrated available capabilities.
type CapabilityResolver struct{ candidates CapabilityCandidates }

func NewCapabilityResolver(candidates CapabilityCandidates) *CapabilityResolver {
	return &CapabilityResolver{candidates: candidates}
}

func (r *CapabilityResolver) Resolve(ctx context.Context, requirement CapabilityRequirement) (ResolvedCapability, error) {
	if err := ctx.Err(); err != nil {
		return ResolvedCapability{}, err
	}
	if r == nil || r.candidates == nil {
		return ResolvedCapability{}, errors.New("migration capability resolver: candidates port is required")
	}
	if requirement.Capability == "" {
		return ResolvedCapability{}, errors.New("migration capability resolver: capability is required")
	}
	candidates, err := r.candidates.Candidates(ctx, requirement)
	if err != nil {
		return ResolvedCapability{}, fmt.Errorf("migration capability resolver: list candidates: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return ResolvedCapability{}, err
	}

	eligible := make([]CapabilityCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return ResolvedCapability{}, err
		}
		if candidate.ID == "" || candidate.Capability != requirement.Capability || !demonstrated(candidate.Evidence) || !validSHA256Digest(candidate.Digest) {
			continue
		}
		compatibility, reasons := assess(requirement, candidate)
		candidate.Compatibility, candidate.Reasons = compatibility, reasons
		if compatibility == CompatibilityUnsupported || (compatibility == CompatibilityDegraded && !requirement.AllowDegraded) {
			continue
		}
		eligible = append(eligible, candidate)
	}
	if len(eligible) == 0 {
		return ResolvedCapability{}, fmt.Errorf("migration capability resolver: no available candidate for %q", requirement.Capability)
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if eligible[i].Compatibility != eligible[j].Compatibility {
			return compatibilityRank(eligible[i].Compatibility) > compatibilityRank(eligible[j].Compatibility)
		}
		if eligible[i].Version != eligible[j].Version {
			return eligible[i].Version > eligible[j].Version
		}
		if eligible[i].Digest != eligible[j].Digest {
			return eligible[i].Digest < eligible[j].Digest
		}
		return eligible[i].ID < eligible[j].ID
	})
	selected := eligible[0]
	return ResolvedCapability{CandidateID: selected.ID, Digest: selected.Digest, Compatibility: selected.Compatibility, Reasons: append([]string(nil), selected.Reasons...)}, nil
}

func validSHA256Digest(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+64 || value[:len(prefix)] != prefix {
		return false
	}
	_, err := hex.DecodeString(value[len(prefix):])
	return err == nil
}

func compatibilityRank(value Compatibility) int {
	switch value {
	case CompatibilityDirect:
		return 4
	case CompatibilityAdaptable:
		return 3
	case CompatibilityDegraded:
		return 2
	default:
		return 1
	}
}

func demonstrated(e CapabilityEvidence) bool {
	return e.Declared && e.Discovered && e.Negotiated && e.Verified && e.Available
}

func assess(requirement CapabilityRequirement, candidate CapabilityCandidate) (Compatibility, []string) {
	var reasons []string
	if requirement.Version != "" && candidate.Version != requirement.Version {
		reasons = append(reasons, "version mismatch")
	}
	if !containsAll(candidate.Platforms, requirement.Platforms) {
		reasons = append(reasons, "platform unavailable")
	}
	if !containsAll(candidate.Permissions.Network, requirement.Permissions.Network) ||
		!containsAll(candidate.Permissions.Filesystem, requirement.Permissions.Filesystem) ||
		!containsAll(candidate.Permissions.Secrets, requirement.Permissions.Secrets) {
		reasons = append(reasons, "permissions insufficient")
	}
	if len(reasons) != 0 {
		return CompatibilityUnsupported, reasons
	}
	if candidate.Compatibility == CompatibilityUnsupported {
		return CompatibilityUnsupported, append([]string(nil), candidate.Reasons...)
	}
	compatibility := candidate.Compatibility
	if compatibility == "" {
		compatibility = CompatibilityDirect
	}
	return compatibility, append([]string(nil), candidate.Reasons...)
}

func containsAll(have, required []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, value := range have {
		set[value] = struct{}{}
	}
	for _, value := range required {
		if _, ok := set[value]; !ok {
			return false
		}
	}
	return true
}

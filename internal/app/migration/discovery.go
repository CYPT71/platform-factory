package migration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	domainmigration "github.com/CYPT71/platform-factory/internal/migration"
)

const maxDiscoveryPages = 100000

// DiscoverySource is the narrow inward-facing port implemented by source
// adapters. Observations crossing this boundary must already be normalized;
// target selection is deliberately absent from discovery.
type DiscoverySource interface {
	SourceID() string
	DiscoverPage(context.Context, string) (DiscoveryPage, error)
}

// DiscoveryPage is one normalized page. An empty NextCursor terminates
// pagination. Status is evidence reported for this page, not an assumption
// made from the absence of observations.
type DiscoveryPage struct {
	Status     domainmigration.DiscoveryStatus
	Resources  []domainmigration.Resource
	Edges      []domainmigration.DependencyEdge
	Unknowns   []domainmigration.UnknownObservation
	Gaps       []domainmigration.CompatibilityGap
	NextCursor string
}

type DiscoveryFailureKind string

const (
	DiscoveryPermissionDenied DiscoveryFailureKind = "permission-denied"
	DiscoveryProviderFailure  DiscoveryFailureKind = "provider-failure"
)

// DiscoveryFailure lets an adapter preserve an explicit unknown scope without
// translating permission denial or provider failure into an empty discovery.
type DiscoveryFailure struct {
	Kind   DiscoveryFailureKind
	Scope  string
	Reason string
	Err    error
}

func (e *DiscoveryFailure) Error() string {
	if e == nil {
		return "migration discovery failed"
	}
	return "migration discovery failed: " + e.Reason
}

func (e *DiscoveryFailure) Unwrap() error { return e.Err }

type Discoverer struct{}

func NewDiscoverer() *Discoverer { return &Discoverer{} }

// Discover exhausts a source independently of any target, validates the
// resulting domain aggregate, and returns it in canonical order.
func (d *Discoverer) Discover(ctx context.Context, source DiscoverySource) (domainmigration.Aggregate, error) {
	if source == nil {
		return failedAggregate("unknown", "source", "configuration", "discovery source is required"), errors.New("migration discovery source is required")
	}
	sourceID := strings.TrimSpace(source.SourceID())
	if sourceID == "" || strings.IndexByte(sourceID, 0) >= 0 {
		return failedAggregate("unknown", "source", "configuration", "discovery source ID is invalid"), errors.New("migration discovery source ID is invalid")
	}

	aggregate := domainmigration.Aggregate{Discovery: domainmigration.DiscoveryComplete}
	seenCursors := map[string]struct{}{"": {}}
	cursor := ""
	for pageNumber := 0; pageNumber < maxDiscoveryPages; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return aggregate.Canonical(), err
		}
		page, err := source.DiscoverPage(ctx, cursor)
		if err != nil {
			failure := classifyDiscoveryFailure(sourceID, err)
			aggregate.Discovery = domainmigration.DiscoveryFailed
			aggregate.Unknowns = append(aggregate.Unknowns, failure)
			return aggregate.Canonical(), fmt.Errorf("discover from %q: %w", sourceID, err)
		}
		if page.Status != domainmigration.DiscoveryComplete && page.Status != domainmigration.DiscoveryPartial && page.Status != domainmigration.DiscoveryFailed {
			return malformedDiscovery(aggregate, sourceID, "provider returned an invalid discovery status")
		}
		aggregate.Resources = append(aggregate.Resources, page.Resources...)
		aggregate.Edges = append(aggregate.Edges, page.Edges...)
		aggregate.Unknowns = append(aggregate.Unknowns, page.Unknowns...)
		aggregate.Gaps = append(aggregate.Gaps, page.Gaps...)
		aggregate.Discovery = mergeDiscoveryStatus(aggregate.Discovery, page.Status)
		if page.Status == domainmigration.DiscoveryFailed {
			if len(page.Unknowns) == 0 {
				return malformedDiscovery(aggregate, sourceID, "failed discovery omitted its unknown scope and reason")
			}
			canonical := aggregate.Canonical()
			if err := canonical.Validate(); err != nil {
				return malformedDiscovery(aggregate, sourceID, err.Error())
			}
			return canonical, errors.New("migration discovery provider reported failure")
		}
		if page.NextCursor == "" {
			canonical := aggregate.Canonical()
			if err := canonical.Validate(); err != nil {
				return malformedDiscovery(aggregate, sourceID, err.Error())
			}
			return canonical, nil
		}
		if len(page.NextCursor) > 1024 || strings.IndexByte(page.NextCursor, 0) >= 0 {
			return malformedDiscovery(aggregate, sourceID, "provider returned an invalid continuation cursor")
		}
		if _, exists := seenCursors[page.NextCursor]; exists {
			return malformedDiscovery(aggregate, sourceID, "provider repeated a continuation cursor")
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	return malformedDiscovery(aggregate, sourceID, "provider exceeded the discovery page limit")
}

func mergeDiscoveryStatus(left, right domainmigration.DiscoveryStatus) domainmigration.DiscoveryStatus {
	if left == domainmigration.DiscoveryFailed || right == domainmigration.DiscoveryFailed {
		return domainmigration.DiscoveryFailed
	}
	if left == domainmigration.DiscoveryPartial || right == domainmigration.DiscoveryPartial {
		return domainmigration.DiscoveryPartial
	}
	return domainmigration.DiscoveryComplete
}

func classifyDiscoveryFailure(sourceID string, err error) domainmigration.UnknownObservation {
	kind, scope, reason := string(DiscoveryProviderFailure), "provider", "source discovery failed"
	var failure *DiscoveryFailure
	if errors.As(err, &failure) {
		if failure.Kind == DiscoveryPermissionDenied || failure.Kind == DiscoveryProviderFailure {
			kind = string(failure.Kind)
		}
		if strings.TrimSpace(failure.Scope) != "" {
			scope = failure.Scope
		}
		if strings.TrimSpace(failure.Reason) != "" {
			reason = failure.Reason
		}
	}
	return domainmigration.UnknownObservation{Source: sourceID, Kind: kind, Scope: scope, Reason: reason}
}

func malformedDiscovery(aggregate domainmigration.Aggregate, sourceID, reason string) (domainmigration.Aggregate, error) {
	aggregate.Discovery = domainmigration.DiscoveryFailed
	aggregate.Unknowns = append(aggregate.Unknowns, domainmigration.UnknownObservation{
		Source: sourceID, Kind: "malformed-observation", Scope: "provider-response", Reason: reason,
	})
	return aggregate.Canonical(), fmt.Errorf("malformed migration discovery from %q: %s", sourceID, reason)
}

func failedAggregate(source, kind, scope, reason string) domainmigration.Aggregate {
	return domainmigration.Aggregate{Discovery: domainmigration.DiscoveryFailed, Unknowns: []domainmigration.UnknownObservation{{
		Source: source, Kind: kind, Scope: scope, Reason: reason,
	}}}
}

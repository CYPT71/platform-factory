// Package placement decides which pending lease a requesting worker
// should run next. It is the piece of the distributed control plane's
// scheduling logic that the roadmap calls out to extract as its own
// component (Next-Generation-Implementation-Roadmap.md, "Contrôle et
// workers"): a pure, worker/state-decoupled decision - platform match,
// capability match, then a cache-locality preference - independent of
// internal/control's lease bookkeeping (assignment, completion,
// persistence) and independent of internal/scheduler's unrelated
// same-process pipeline-stage DAG scheduler.
package placement

import "sort"

// Worker is the placement-relevant subset of a worker's advertised
// state. Capabilities and CachedContent must be sorted (internal/control
// already keeps both sorted on write; Select does not sort them itself,
// so a caller with unsorted input will silently get wrong answers rather
// than an error - this mirrors internal/control's own containsSorted
// precondition instead of hiding it behind a redundant sort here).
type Worker struct {
	Platform      string
	Capabilities  []string
	CachedContent []string
}

// Candidate is the placement-relevant subset of one pending lease.
type Candidate struct {
	RequiredPlatform     string
	RequiredCapabilities []string
	PreferredContent     string
	// Priority orders otherwise-tied candidates: higher wins. It is a
	// submission-time value (the tenant's configured priority at the
	// moment the lease was submitted, per internal/quota.FairScheduler.
	// GetPriority), not re-resolved dynamically - consistent with
	// MaxParallel quota enforcement, which is likewise checked once at
	// submission, not continuously. Zero (the default for a lease with
	// no tenant, or before this field existed) never outranks anything
	// and is never outranked by another zero, so callers that never set
	// it get exactly the old cache-locality-then-FIFO behavior.
	Priority int
}

// Select returns the index into pending of the lease worker should run
// next, choosing among leases worker is eligible for by, in order:
// highest Priority; then whether PreferredContent is already in worker's
// CachedContent (cache locality); then submission order (FIFO) - so
// among equal-priority candidates, an earlier submission is a fairness
// tie-breaker, not an accident of map iteration. ok is false if worker is
// eligible for none of pending.
func Select(worker Worker, pending []Candidate) (index int, ok bool) {
	best := -1
	for i, candidate := range pending {
		if !Eligible(worker, candidate) {
			continue
		}
		if best < 0 || betterCandidate(worker, candidate, pending[best]) {
			best = i
		}
	}
	if best < 0 {
		return 0, false
	}
	return best, true
}

// betterCandidate reports whether candidate should be preferred over
// current under worker's cache state. Called only between two already-
// eligible candidates; "better" is priority first, cache locality second
// - index/FIFO order is handled by Select never overwriting best on a
// tie (the earlier candidate it already holds stays selected).
func betterCandidate(worker Worker, candidate, current Candidate) bool {
	if candidate.Priority != current.Priority {
		return candidate.Priority > current.Priority
	}
	candidateCached := candidate.PreferredContent != "" && containsSorted(worker.CachedContent, candidate.PreferredContent)
	currentCached := current.PreferredContent != "" && containsSorted(worker.CachedContent, current.PreferredContent)
	return candidateCached && !currentCached
}

// Eligible reports whether worker satisfies candidate's platform and
// capability requirements, independent of cache locality or ordering.
func Eligible(worker Worker, candidate Candidate) bool {
	if candidate.RequiredPlatform != "" && candidate.RequiredPlatform != worker.Platform {
		return false
	}
	for _, capability := range candidate.RequiredCapabilities {
		if !containsSorted(worker.Capabilities, capability) {
			return false
		}
	}
	return true
}

func containsSorted(values []string, wanted string) bool {
	index := sort.SearchStrings(values, wanted)
	return index < len(values) && values[index] == wanted
}

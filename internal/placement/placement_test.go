package placement

import "testing"

func TestSelectRejectsPlatformMismatch(t *testing.T) {
	worker := Worker{Platform: "linux/amd64"}
	pending := []Candidate{{RequiredPlatform: "linux/arm64"}}
	if _, ok := Select(worker, pending); ok {
		t.Fatal("selected a lease requiring an incompatible platform")
	}
}

func TestSelectRejectsMissingCapability(t *testing.T) {
	worker := Worker{Platform: "linux/amd64", Capabilities: []string{"cache", "kvm"}}
	pending := []Candidate{{RequiredPlatform: "linux/amd64", RequiredCapabilities: []string{"gpu"}}}
	if _, ok := Select(worker, pending); ok {
		t.Fatal("selected a lease requiring a capability the worker lacks")
	}
}

func TestSelectAcceptsWorkerWithAllRequiredCapabilities(t *testing.T) {
	worker := Worker{Platform: "linux/amd64", Capabilities: []string{"cache", "gpu", "kvm"}}
	pending := []Candidate{{RequiredPlatform: "linux/amd64", RequiredCapabilities: []string{"gpu", "kvm"}}}
	index, ok := Select(worker, pending)
	if !ok || index != 0 {
		t.Fatalf("index=%d ok=%v", index, ok)
	}
}

func TestSelectEmptyRequiredPlatformMatchesAnyWorker(t *testing.T) {
	worker := Worker{Platform: "linux/arm64"}
	pending := []Candidate{{RequiredPlatform: ""}}
	if _, ok := Select(worker, pending); !ok {
		t.Fatal("empty RequiredPlatform should match any worker")
	}
}

func TestSelectReturnsFalseForEmptyPending(t *testing.T) {
	if _, ok := Select(Worker{Platform: "linux/amd64"}, nil); ok {
		t.Fatal("selected from an empty pending list")
	}
}

func TestSelectPrefersFIFOAmongEquallyEligibleCandidates(t *testing.T) {
	worker := Worker{Platform: "linux/amd64"}
	pending := []Candidate{
		{RequiredPlatform: "linux/amd64"},
		{RequiredPlatform: "linux/amd64"},
	}
	index, ok := Select(worker, pending)
	if !ok || index != 0 {
		t.Fatalf("want the first eligible candidate, got index=%d ok=%v", index, ok)
	}
}

func TestSelectSkipsIneligibleCandidatesToReachAnEligibleOne(t *testing.T) {
	worker := Worker{Platform: "linux/amd64"}
	pending := []Candidate{
		{RequiredPlatform: "linux/arm64"}, // ineligible, must be skipped
		{RequiredPlatform: "linux/amd64"}, // this one
	}
	index, ok := Select(worker, pending)
	if !ok || index != 1 {
		t.Fatalf("index=%d ok=%v", index, ok)
	}
}

// TestSelectPrefersCacheLocalityOverFIFOOrder is the property that makes
// this more than a first-match scan: a later, equally-eligible candidate
// whose content the worker already has cached wins over an earlier one
// that would force a cold fetch.
func TestSelectPrefersCacheLocalityOverFIFOOrder(t *testing.T) {
	worker := Worker{Platform: "linux/amd64", CachedContent: []string{"layer-b"}}
	pending := []Candidate{
		{RequiredPlatform: "linux/amd64", PreferredContent: "layer-a"}, // first, but cold
		{RequiredPlatform: "linux/amd64", PreferredContent: "layer-b"}, // cached
	}
	index, ok := Select(worker, pending)
	if !ok || index != 1 {
		t.Fatalf("want the cache-local candidate at index 1, got index=%d ok=%v", index, ok)
	}
}

func TestSelectFallsBackToFIFOWhenNoCandidateIsCached(t *testing.T) {
	worker := Worker{Platform: "linux/amd64", CachedContent: []string{"layer-z"}}
	pending := []Candidate{
		{RequiredPlatform: "linux/amd64", PreferredContent: "layer-a"},
		{RequiredPlatform: "linux/amd64", PreferredContent: "layer-b"},
	}
	index, ok := Select(worker, pending)
	if !ok || index != 0 {
		t.Fatalf("want FIFO fallback at index 0, got index=%d ok=%v", index, ok)
	}
}

// TestSelectPrefersHigherPriorityOverSubmissionOrder proves priority
// actually overrides plain FIFO fairness: a later-submitted, higher
// priority candidate wins over an earlier, lower-priority one.
func TestSelectPrefersHigherPriorityOverSubmissionOrder(t *testing.T) {
	worker := Worker{Platform: "linux/amd64"}
	pending := []Candidate{
		{RequiredPlatform: "linux/amd64", Priority: 0},  // submitted first, default priority
		{RequiredPlatform: "linux/amd64", Priority: 10}, // submitted second, higher priority
	}
	index, ok := Select(worker, pending)
	if !ok || index != 1 {
		t.Fatalf("want the higher-priority candidate at index 1, got index=%d ok=%v", index, ok)
	}
}

// TestSelectPriorityBeatsCacheLocality proves the tie-break order is
// priority first, then cache locality: a higher-priority candidate wins
// even if a lower-priority one is already cached.
func TestSelectPriorityBeatsCacheLocality(t *testing.T) {
	worker := Worker{Platform: "linux/amd64", CachedContent: []string{"layer-cached"}}
	pending := []Candidate{
		{RequiredPlatform: "linux/amd64", PreferredContent: "layer-cached", Priority: 0},
		{RequiredPlatform: "linux/amd64", PreferredContent: "layer-cold", Priority: 5},
	}
	index, ok := Select(worker, pending)
	if !ok || index != 1 {
		t.Fatalf("want the higher-priority (cold) candidate at index 1, got index=%d ok=%v", index, ok)
	}
}

// TestSelectFallsBackToCacheLocalityThenFIFOAmongEqualPriority proves
// that among candidates of equal priority (including the common all-zero
// case), the existing cache-locality-then-FIFO order still governs -
// priority only breaks ties, it doesn't replace the rest of the policy.
func TestSelectFallsBackToCacheLocalityThenFIFOAmongEqualPriority(t *testing.T) {
	worker := Worker{Platform: "linux/amd64", CachedContent: []string{"layer-b"}}
	pending := []Candidate{
		{RequiredPlatform: "linux/amd64", PreferredContent: "layer-a", Priority: 3},
		{RequiredPlatform: "linux/amd64", PreferredContent: "layer-b", Priority: 3},
	}
	index, ok := Select(worker, pending)
	if !ok || index != 1 {
		t.Fatalf("want the equal-priority cache-local candidate at index 1, got index=%d ok=%v", index, ok)
	}
}

// TestSelectNegativePriorityLosesToDefault proves priority is a real
// ordering, not just a positive-only boost: an explicitly deprioritized
// (negative) candidate loses to a default (zero) one submitted later.
func TestSelectNegativePriorityLosesToDefault(t *testing.T) {
	worker := Worker{Platform: "linux/amd64"}
	pending := []Candidate{
		{RequiredPlatform: "linux/amd64", Priority: -5},
		{RequiredPlatform: "linux/amd64", Priority: 0},
	}
	index, ok := Select(worker, pending)
	if !ok || index != 1 {
		t.Fatalf("want the default-priority candidate at index 1, got index=%d ok=%v", index, ok)
	}
}

func TestEligibleIsExposedIndependentlyOfSelection(t *testing.T) {
	worker := Worker{Platform: "linux/amd64", Capabilities: []string{"kvm"}}
	if !Eligible(worker, Candidate{RequiredPlatform: "linux/amd64", RequiredCapabilities: []string{"kvm"}}) {
		t.Fatal("expected eligible")
	}
	if Eligible(worker, Candidate{RequiredCapabilities: []string{"gpu"}}) {
		t.Fatal("expected ineligible")
	}
}

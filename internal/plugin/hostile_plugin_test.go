//go:build linux

package plugin

import (
	"context"
	"testing"
)

// TestHostileMemoryBombIsBlockedByRLIMIT_AS proves permission_profile.go's
// per-family memory ceiling (§12's "Limit memory with runtime-compatible
// mechanism") against a plugin that actively tries to violate it, not just
// one that reports what the ceiling is (that's
// TestStartWithFamilyAppliesPermissionProfile). A positive control
// (comfortably under the ceiling) must succeed normally; the hostile case
// (comfortably over it) must not - the plugin process itself dies
// attempting the over-ceiling allocation (Go's runtime treats an
// allocation failure as fatal, not a recoverable error), so the RPC call
// observes that as a failure, not a graceful in-band error result.
func TestHostileMemoryBombIsBlockedByRLIMIT_AS(t *testing.T) {
	binary, err := sandboxProbePluginPath()
	if err != nil {
		t.Fatalf("build sandbox probe plugin: %v", err)
	}
	// PluginFamilyBuild has the largest configured ceiling (4096 MiB,
	// permission_profile.go), giving the most headroom above the Go
	// runtime's own boot footprint to work with.
	ceiling := int64(permissionProfiles[PluginFamilyBuild].MemoryMiB) << 20
	// 64 MiB on top of boot overhead is comfortably under any of this
	// package's measured ceilings (see permission_profile.go's doc comment:
	// boot alone needs on the order of 1 GiB, and every configured ceiling
	// sits with real margin above that) - unlike a naive ceiling/4, which a
	// real run of this test caught actually failing: boot overhead plus a
	// 512 MiB additional commit exceeded the 2048 MiB language-family
	// ceiling used in an earlier version of this test.
	const withinLimit = 64 << 20

	t.Run("under the ceiling succeeds", func(t *testing.T) {
		client, err := StartWithFamily(context.Background(), binary, nil, nil, PluginFamilyBuild)
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		defer client.Close()
		var result map[string]int64
		if err := client.Call(context.Background(), "v1.observe.memory-bomb",
			map[string]int64{"bytes": withinLimit}, &result); err != nil {
			t.Fatalf("an allocation comfortably under the RLIMIT_AS ceiling (%d bytes) failed: %v", int64(withinLimit), err)
		}
		if result["committed_bytes"] != withinLimit {
			t.Fatalf("committed_bytes=%d, want %d", result["committed_bytes"], int64(withinLimit))
		}
	})

	t.Run("over the ceiling is blocked", func(t *testing.T) {
		client, err := StartWithFamily(context.Background(), binary, nil, nil, PluginFamilyBuild)
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		defer client.Close()
		var result map[string]int64
		attempt := ceiling * 2
		if err := client.Call(context.Background(), "v1.observe.memory-bomb",
			map[string]int64{"bytes": attempt}, &result); err == nil {
			t.Fatalf("an allocation of %d bytes, well over the RLIMIT_AS ceiling of %d, unexpectedly succeeded (result=%+v) - the plugin memory ceiling is not being enforced", attempt, ceiling, result)
		}
	})
}

// TestHostileForkBombIsBoundedByRLIMIT_NPROC proves §12's "Limit PIDs"
// against a plugin that actively tries to have more processes alive at
// once than its profile allows, rather than one that only reports the
// configured limit (TestStartWithFamilyAppliesPermissionProfile). Unlike
// the memory case, exceeding RLIMIT_NPROC does not crash the plugin -
// individual fork/clone calls past the ceiling simply fail with EAGAIN -
// so the assertion is on the reported failure count, not on the RPC call
// itself failing.
func TestHostileForkBombIsBoundedByRLIMIT_NPROC(t *testing.T) {
	binary, err := sandboxProbePluginPath()
	if err != nil {
		t.Fatalf("build sandbox probe plugin: %v", err)
	}
	profile := permissionProfiles[PluginFamilyBuild]
	attempts := int(profile.Processes) + 40

	client, err := StartWithFamily(context.Background(), binary, nil, nil, PluginFamilyBuild)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer client.Close()

	var result map[string]int
	if err := client.Call(context.Background(), "v1.observe.fork-bomb",
		map[string]int{"attempts": attempts}, &result); err != nil {
		t.Fatalf("call: %v", err)
	}
	if result["failed"] == 0 {
		t.Fatalf("attempting %d concurrent children against a RLIMIT_NPROC of %d produced zero failures (result=%+v) - the process ceiling is not being enforced", attempts, profile.Processes, result)
	}
	if result["succeeded"] > int(profile.Processes) {
		t.Fatalf("%d children started concurrently, more than the configured RLIMIT_NPROC of %d", result["succeeded"], profile.Processes)
	}
}

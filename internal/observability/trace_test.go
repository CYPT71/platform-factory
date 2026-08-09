package observability

import (
	"fmt"
	"strings"
	"testing"
)

func TestNewTraceID_Format(t *testing.T) {
	// Test basic format: v1-{origin}-{command}-{24_hex_chars}
	traceID := NewTraceID("cli", "build")

	// Check prefix
	if !strings.HasPrefix(string(traceID), "v1-cli-build-") {
		t.Fatalf("expected prefix 'v1-cli-build-', got %q", traceID)
	}

	// Check total length (v1- = 3, origin + command vary, - = 1, hash = 24)
	// Minimum: v1-a-b-000000000000000000000000 (3 + 1 + 1 + 1 + 24 = 30)
	if len(traceID) < 30 {
		t.Fatalf("trace ID too short: %q (len=%d)", traceID, len(traceID))
	}

	// Check hash part is 24 hex characters
	parts := strings.Split(string(traceID), "-")
	if len(parts) != 4 {
		t.Fatalf("expected 4 parts separated by '-', got %d: %q", len(parts), traceID)
	}

	hashPart := parts[3]
	if len(hashPart) != 24 {
		t.Fatalf("expected 24-character hash, got %d: %q", len(hashPart), hashPart)
	}

	// Verify hash is hex
	for _, c := range hashPart {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("non-hex character in hash: %q", c)
		}
	}
}

func TestNewTraceID_Unique(t *testing.T) {
	// Generate many trace IDs and ensure they're all unique
	seen := make(map[TraceID]bool)
	for i := 0; i < 10000; i++ {
		// Only 10 different commands to stress the counter
		traceID := NewTraceID("test", fmt.Sprintf("cmd%d", i%10))
		if seen[traceID] {
			t.Fatalf("duplicate trace ID generated: %s", traceID)
		}
		seen[traceID] = true
	}
}

func TestNewTraceID_DifferentOrigins(t *testing.T) {
	traceID1 := NewTraceID("cli", "build")
	traceID2 := NewTraceID("api", "build")

	if traceID1 == traceID2 {
		t.Fatalf("same trace ID for different origins: %s", traceID1)
	}

	// Check origins are different in the string
	if !strings.Contains(string(traceID1), "cli") {
		t.Fatalf("expected 'cli' in trace ID: %s", traceID1)
	}
	if !strings.Contains(string(traceID2), "api") {
		t.Fatalf("expected 'api' in trace ID: %s", traceID2)
	}
}

func TestNewTraceID_DifferentCommands(t *testing.T) {
	traceID1 := NewTraceID("cli", "build")
	traceID2 := NewTraceID("cli", "deploy")

	if traceID1 == traceID2 {
		t.Fatalf("same trace ID for different commands: %s", traceID1)
	}

	// Check commands are different in the string
	if !strings.Contains(string(traceID1), "build") {
		t.Fatalf("expected 'build' in trace ID: %s", traceID1)
	}
	if !strings.Contains(string(traceID2), "deploy") {
		t.Fatalf("expected 'deploy' in trace ID: %s", traceID2)
	}
}

func TestParseTraceID_Valid(t *testing.T) {
	traceID := NewTraceID("cli", "build")

	version, origin, command, hashHex, err := ParseTraceID(string(traceID))
	if err != nil {
		t.Fatalf("ParseTraceID failed: %v", err)
	}

	if version != "v1" {
		t.Fatalf("expected version 'v1', got %q", version)
	}

	if origin != "cli" {
		t.Fatalf("expected origin 'cli', got %q", origin)
	}

	if command != "build" {
		t.Fatalf("expected command 'build', got %q", command)
	}

	if len(hashHex) != 24 {
		t.Fatalf("expected 24-character hash, got %d: %q", len(hashHex), hashHex)
	}
}

func TestParseTraceID_Invalid(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{"too short", "v1-a"},
		{"missing version", "cli-build-000000000000000000000000"},
		{"invalid version", "x1-cli-build-000000000000000000000000"},
		{"wrong hash length", "v1-cli-build-0000"},
		{"non-hex hash", "v1-cli-build-00000000000000000000000g"},
		{"wrong parts count", "v1-cli-build"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, _, err := ParseTraceID(tt.id)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", tt.id)
			}
		})
	}
}

func TestTraceID_Methods(t *testing.T) {
	traceID := NewTraceID("cli", "build")

	// Test Origin
	origin, ok := traceID.Origin()
	if !ok {
		t.Fatal("expected Origin() to succeed")
	}
	if origin != "cli" {
		t.Fatalf("expected origin 'cli', got %q", origin)
	}

	// Test Command
	command, ok := traceID.Command()
	if !ok {
		t.Fatal("expected Command() to succeed")
	}
	if command != "build" {
		t.Fatalf("expected command 'build', got %q", command)
	}

	// Test Version
	version, ok := traceID.Version()
	if !ok {
		t.Fatal("expected Version() to succeed")
	}
	if version != "v1" {
		t.Fatalf("expected version 'v1', got %q", version)
	}

	// Test HashHex
	hashHex, ok := traceID.HashHex()
	if !ok {
		t.Fatal("expected HashHex() to succeed")
	}
	if len(hashHex) != 24 {
		t.Fatalf("expected 24-character hash, got %d: %q", len(hashHex), hashHex)
	}
}

func TestTraceID_Methods_Invalid(t *testing.T) {
	invalidID := TraceID("invalid")

	if _, ok := invalidID.Origin(); ok {
		t.Fatal("expected Origin() to fail for invalid ID")
	}

	if _, ok := invalidID.Command(); ok {
		t.Fatal("expected Command() to fail for invalid ID")
	}

	if _, ok := invalidID.Version(); ok {
		t.Fatal("expected Version() to fail for invalid ID")
	}

	if _, ok := invalidID.HashHex(); ok {
		t.Fatal("expected HashHex() to fail for invalid ID")
	}
}

func TestTraceID_String(t *testing.T) {
	traceID := NewTraceID("cli", "build")

	// TraceID is a string type, so it should work as a string
	s := string(traceID)
	if !strings.Contains(s, "v1-cli-build-") {
		t.Fatalf("expected trace ID string to contain 'v1-cli-build-', got %q", s)
	}

	// String() method should return the same
	if traceID.String() != s {
		t.Fatalf("String() method returned different value: %q vs %q", traceID.String(), s)
	}
}

func TestNewTraceID_EmptyOriginAndCommand(t *testing.T) {
	// Empty origin and command should still work
	traceID := NewTraceID("", "")

	// Should still have the format: v1---{hash} (3 dashes: v1- - -hash)
	if !strings.HasPrefix(string(traceID), "v1---") {
		t.Fatalf("expected prefix 'v1---', got %q", traceID)
	}

	// Parse should still work
	version, origin, command, hashHex, err := ParseTraceID(string(traceID))
	if err != nil {
		t.Fatalf("ParseTraceID failed: %v", err)
	}

	if version != "v1" {
		t.Fatalf("expected version 'v1', got %q", version)
	}

	if origin != "" {
		t.Fatalf("expected empty origin, got %q", origin)
	}

	if command != "" {
		t.Fatalf("expected empty command, got %q", command)
	}

	if len(hashHex) != 24 {
		t.Fatalf("expected 24-character hash, got %d: %q", len(hashHex), hashHex)
	}
}

func TestNewTraceID_Robustness(t *testing.T) {
	// Test with various origin and command combinations
	origins := []string{"cli", "api", "test", "scheduler", "plugin", ""}
	commands := []string{"build", "deploy", "publish", "run", "stop", ""}

	seen := make(map[TraceID]bool)
	for _, origin := range origins {
		for _, command := range commands {
			traceID := NewTraceID(origin, command)
			if seen[traceID] {
				t.Fatalf("duplicate trace ID for origin=%q, command=%q: %s", origin, command, traceID)
			}
			seen[traceID] = true

			// Verify it can be parsed back
			v, o, c, h, err := ParseTraceID(string(traceID))
			if err != nil {
				t.Fatalf("failed to parse generated trace ID %s: %v", traceID, err)
			}
			if v != "v1" || o != origin || c != command || len(h) != 24 {
				t.Fatalf("parsed values don't match: got v=%q o=%q c=%q h=%q", v, o, c, h)
			}
		}
	}
}

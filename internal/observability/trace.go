// Package observability provides cross-cutting concerns like tracing and metrics.
// This file implements the robust trace ID generation as specified in
// Sanetizer-todo.md item 18 for end-to-end correlation.
package observability

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// TraceIDVersion is the current version of the trace ID format.
// Increment this to change the format without breaking existing parsers.
const TraceIDVersion = "v1"

// traceIDHashBytes is the number of bytes to use from the SHA-256 hash.
// 12 bytes = 24 hex characters.
const traceIDHashBytes = 12

var (
	// processStartTime is captured once at package load time.
	// It never changes, even if the system clock is adjusted later.
	processStartTime = time.Now().UnixNano()

	// processCounter is atomically incremented on every NewTraceID call.
	// This guarantees uniqueness within the same process.
	processCounter uint64

	// processHostname is computed once at package load time.
	processHostname = getHostname()

	// processHostnameHash is the first 4 bytes of SHA-256(hostname).
	// Computed once to avoid repeated hashing.
	processHostnameHash = computeHostnameHash()
)

func getHostname() string {
	hostname, _ := os.Hostname()
	return hostname
}

func computeHostnameHash() [4]byte {
	h := sha256.Sum256([]byte(processHostname))
	var result [4]byte
	copy(result[:], h[:4])
	return result
}

// TraceID is a unique, traceable identifier for operations.
// Format: v1-{origin}-{command}-{24_hex_chars}
// Example: v1-cli-build-9f86d081884c7d657a89ce95
//
// The hash is computed from:
// - TraceIDVersion (for forward compatibility)
// - origin (who is calling: cli, api, test, scheduler)
// - command (what action: build, deploy, publish)
// - PID (4 bytes, little-endian)
// - Process start time in nanoseconds (8 bytes)
// - Atomic counter (8 bytes, incremented per call)
// - Hostname hash (4 bytes, first 4 bytes of SHA-256(hostname))
// - User ID (4 bytes)
//
// This ensures global uniqueness and resilience to:
// - Clock adjustments (NTP, manual)
// - PID reuse
// - High-frequency calls
// - Multi-host environments
// - Multi-user environments
type TraceID string

// NewTraceID generates a globally unique, robust trace ID.
// The returned TraceID is guaranteed to be unique across:
// - Different processes (PID + StartTime + Hostname + UID)
// - Different calls within the same process (atomic counter)
// - Different machines (Hostname hash + UID)
// - Different users (UID)
//
// The format is: v1-{origin}-{command}-{24_hex_chars}
//
// This implements Sanetizer-todo.md item 18 requirement for a trace_id
// that is "le plus robuste possible et le plus resillant" and must
// "tenir min 10 ans".
func NewTraceID(origin, command string) TraceID {
	counter := atomic.AddUint64(&processCounter, 1)

	h := sha256.New()

	// 1. Version (for forward compatibility)
	h.Write([]byte(TraceIDVersion))
	h.Write([]byte{0}) // null separator

	// 2. Origin (who is calling: cli, api, test, scheduler)
	h.Write([]byte(origin))
	h.Write([]byte{0}) // null separator

	// 3. Command (what action: build, deploy, publish)
	h.Write([]byte(command))
	h.Write([]byte{0}) // null separator

	// 4. PID (4 bytes, little-endian)
	var pidBuf [4]byte
	binary.LittleEndian.PutUint32(pidBuf[:], uint32(os.Getpid()))
	h.Write(pidBuf[:])

	// 5. Process start time (8 bytes, nanoseconds since epoch)
	var startBuf [8]byte
	binary.LittleEndian.PutUint64(startBuf[:], uint64(processStartTime))
	h.Write(startBuf[:])

	// 6. Atomic counter (8 bytes)
	var counterBuf [8]byte
	binary.LittleEndian.PutUint64(counterBuf[:], counter)
	h.Write(counterBuf[:])

	// 7. Hostname hash (4 bytes, pre-computed)
	h.Write(processHostnameHash[:])

	// 8. User ID (4 bytes)
	var uidBuf [4]byte
	binary.LittleEndian.PutUint32(uidBuf[:], uint32(os.Getuid()))
	h.Write(uidBuf[:])

	// Compute hash and take first traceIDHashBytes
	sum := h.Sum(nil)
	return TraceID(fmt.Sprintf("%s-%s-%s-%x",
		TraceIDVersion, origin, command, sum[:traceIDHashBytes]))
}

// String returns the trace ID as a string (implements fmt.Stringer).
func (t TraceID) String() string {
	return string(t)
}

// ParseTraceID extracts components from a trace ID string.
// Returns the version, origin, command, hash hex, and any parsing error.
// The input must match the format: v{version}-{origin}-{command}-{24_hex_chars}
func ParseTraceID(id string) (version, origin, command, hashHex string, err error) {
	// Minimum length: v1-a-b-000000000000 (13 chars)
	// Expected length: v1-orig-cmd-000000000000000000000000 (varies based on origin/command)
	// We need at least: v1-- -12 hex = 16 chars minimum
	if len(id) < 16 {
		return "", "", "", "", fmt.Errorf("trace ID too short: %q", id)
	}

	// Split by '-' - we expect exactly 4 parts
	var parts []string
	start := 0
	for i := 0; i < len(id); i++ {
		if id[i] == '-' {
			parts = append(parts, id[start:i])
			start = i + 1
		}
	}
	parts = append(parts, id[start:])

	if len(parts) != 4 {
		return "", "", "", "", fmt.Errorf("trace ID must have 4 hyphen-separated parts, got %d: %q", len(parts), id)
	}

	version = parts[0]
	origin = parts[1]
	command = parts[2]
	hashHex = parts[3]

	// Validate version starts with 'v'
	if len(version) < 1 || version[0] != 'v' {
		return "", "", "", "", fmt.Errorf("invalid version format: %q", version)
	}

	// Validate hash is 24 hex characters
	if len(hashHex) != 24 {
		return "", "", "", "", fmt.Errorf("hash must be 24 hex characters, got %d: %q", len(hashHex), hashHex)
	}

	for _, c := range hashHex {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return "", "", "", "", fmt.Errorf("hash contains non-hex character: %q", hashHex)
		}
	}

	return version, origin, command, hashHex, nil
}

// Origin returns the origin part of the trace ID (e.g., "cli", "api", "test").
func (t TraceID) Origin() (string, bool) {
	version, origin, _, _, err := ParseTraceID(string(t))
	if err != nil {
		return "", false
	}
	_ = version // ignored for now
	return origin, true
}

// Command returns the command part of the trace ID (e.g., "build", "deploy").
func (t TraceID) Command() (string, bool) {
	_, _, command, _, err := ParseTraceID(string(t))
	if err != nil {
		return "", false
	}
	return command, true
}

// Version returns the version part of the trace ID (e.g., "v1").
func (t TraceID) Version() (string, bool) {
	version, _, _, _, err := ParseTraceID(string(t))
	if err != nil {
		return "", false
	}
	return version, true
}

// HashHex returns the hex hash part of the trace ID (24 characters).
func (t TraceID) HashHex() (string, bool) {
	_, _, _, hashHex, err := ParseTraceID(string(t))
	if err != nil {
		return "", false
	}
	return hashHex, true
}

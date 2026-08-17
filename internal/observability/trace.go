// Package observability provides structured logs, metrics, traces, and context propagation.
package observability

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// TraceIDVersion identifies the current trace ID format.
const TraceIDVersion = "v1"

const traceIDHashBytes = 12

var (
	processStartTime    = time.Now().UnixNano()
	processCounter      uint64
	processHostname     = getHostname()
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

// TraceID identifies an operation as v1-{origin}-{command}-{24 hex chars}.
type TraceID string

// NewTraceID combines process, host, user, and monotonic call identity.
func NewTraceID(origin, command string) TraceID {
	counter := atomic.AddUint64(&processCounter, 1)
	h := sha256.New()
	h.Write([]byte(TraceIDVersion))
	h.Write([]byte{0})
	h.Write([]byte(origin))
	h.Write([]byte{0})
	h.Write([]byte(command))
	h.Write([]byte{0})
	var pidBuf [4]byte
	binary.LittleEndian.PutUint32(pidBuf[:], uint32(os.Getpid()))
	h.Write(pidBuf[:])
	var startBuf [8]byte
	binary.LittleEndian.PutUint64(startBuf[:], uint64(processStartTime))
	h.Write(startBuf[:])
	var counterBuf [8]byte
	binary.LittleEndian.PutUint64(counterBuf[:], counter)
	h.Write(counterBuf[:])
	h.Write(processHostnameHash[:])
	var uidBuf [4]byte
	binary.LittleEndian.PutUint32(uidBuf[:], uint32(os.Getuid()))
	h.Write(uidBuf[:])
	sum := h.Sum(nil)
	return TraceID(fmt.Sprintf("%s-%s-%s-%x",
		TraceIDVersion, origin, command, sum[:traceIDHashBytes]))
}

// String returns the trace ID.
func (t TraceID) String() string {
	return string(t)
}

// ParseTraceID validates and splits a trace ID.
func ParseTraceID(id string) (version, origin, command, hashHex string, err error) {
	if len(id) < 16 {
		return "", "", "", "", fmt.Errorf("trace ID too short: %q", id)
	}
	parts := strings.Split(id, "-")
	if len(parts) != 4 {
		return "", "", "", "", fmt.Errorf("trace ID must have 4 hyphen-separated parts, got %d: %q", len(parts), id)
	}
	version, origin, command, hashHex = parts[0], parts[1], parts[2], parts[3]
	if version == "" || version[0] != 'v' {
		return "", "", "", "", fmt.Errorf("invalid version format: %q", version)
	}
	if len(hashHex) != 24 {
		return "", "", "", "", fmt.Errorf("hash must be 24 hex characters, got %d: %q", len(hashHex), hashHex)
	}
	if _, err := hex.DecodeString(hashHex); err != nil || hashHex != strings.ToLower(hashHex) {
		return "", "", "", "", fmt.Errorf("hash contains non-hex character: %q", hashHex)
	}
	return version, origin, command, hashHex, nil
}

// Origin returns the origin part of the trace ID (e.g., "cli", "api", "test").
func (t TraceID) Origin() (string, bool) {
	_, origin, _, _, err := ParseTraceID(string(t))
	if err != nil {
		return "", false
	}
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

package v1

import sdk "github.com/CYPT71/platform-factory/sdk/microvm"

// NamePattern is the DNS-label rule every backend validates Spec.Name and
// Spec.Namespace against.
var NamePattern = sdk.NamePattern

// Spec is the common, deliberately small configuration understood by every
// microVM backend. Backend-specific implementation details never belong here.
type Spec = sdk.Spec

// Forward is one host<->guest port forward.
type Forward = sdk.Forward

// ParseForward parses a --publish value: CONTAINER, HOST:CONTAINER, or
// IP:HOST:CONTAINER, each with an optional /tcp or /udp suffix.
func ParseForward(value string) (Forward, error) {
	return sdk.ParseForward(value)
}

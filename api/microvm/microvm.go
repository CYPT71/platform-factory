// Package microvm preserves the original microVM API import path.
//
// Deprecated: use github.com/CYPT71/secure-oci-base/sdk/microvm. This package
// is a source-compatible forwarding shim; its aliases denote the exact SDK
// types and can be exchanged with them without conversion.
package microvm

import (
	"regexp"

	sdk "github.com/CYPT71/secure-oci-base/sdk/microvm"
)

var namePattern = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$`)

// NamePattern is the DNS-label rule every backend validates Spec.Name and
// Spec.Namespace against.
var NamePattern = namePattern

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

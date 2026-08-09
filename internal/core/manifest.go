// Package core contains the domain types for Platform Factory.
// This file implements the plugin manifest schema described in
// Sanetizer-todo.md item 11 for capability-based dispatch.
package core

import (
	"fmt"
)

// PluginFamily is which family of plugin a manifest declares itself as -
// Sanetizer-todo.md item 11: the core should ask "who can provide
// deployment.apply?" (a capability, declared in Capabilities below), never
// "are you KubeVirt?" (a hardcoded identity check). Family exists for
// discovery/filtering, not for the core to branch behavior on a specific
// plugin's name.
type PluginFamily string

const (
	PluginFamilyLanguage   PluginFamily = "language"
	PluginFamilyAnalyzer   PluginFamily = "analyzer"
	PluginFamilyBuild      PluginFamily = "build"
	PluginFamilyRuntime    PluginFamily = "runtime"
	PluginFamilyDeployment PluginFamily = "deployment"
	PluginFamilyCapability PluginFamily = "capability"
)

func (f PluginFamily) valid() bool {
	switch f {
	case PluginFamilyLanguage, PluginFamilyAnalyzer, PluginFamilyBuild,
		PluginFamilyRuntime, PluginFamilyDeployment, PluginFamilyCapability:
		return true
	default:
		return false
	}
}

// PluginPermissions is the least-privilege declaration a manifest makes:
// every capability accessed outside the plugin's own confined workspace
// must be named here, not assumed. An empty PluginPermissions is the
// correct declaration for a plugin that needs none of these - the zero
// value is deliberately the most restrictive one, not "unspecified."
type PluginPermissions struct {
	Network    []string `json:"network,omitempty" yaml:"network,omitempty"`
	Filesystem []string `json:"filesystem,omitempty" yaml:"filesystem,omitempty"`
	Secrets    []string `json:"secrets,omitempty" yaml:"secrets,omitempty"`
}

// PluginManifest is the schema every plugin (language, analyzer, build,
// runtime, deployment, or capability) declares about itself - see
// Sanetizer-todo.md item 11's example YAML. The host validates a
// manifest before ever exec'ing or trusting the plugin it describes;
// nothing about what a plugin claims here is assumed correct without
// this validation passing first, the same "verify, don't trust" pattern
// internal/oci/extralayers.go already applies to plugin-supplied layers.
type PluginManifest struct {
	ID              PluginID          `json:"id" yaml:"id"`
	Version         string            `json:"version" yaml:"version"`
	ProtocolVersion int               `json:"protocol_version" yaml:"protocol_version"`
	Family          PluginFamily      `json:"family" yaml:"family"`
	Capabilities    []string          `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	Permissions     PluginPermissions `json:"permissions,omitempty" yaml:"permissions,omitempty"`
}

// Validate checks the manifest is well-formed: every required field is
// present, the declared family is one this codebase recognizes, and
// there are no duplicate capability entries (a duplicate is always
// either a copy-paste mistake or an attempt to make a capability look
// more heavily supported than it is - reject it rather than silently
// dedup, so the author sees the mistake).
func (m PluginManifest) Validate() error {
	if m.ID == "" {
		return fmt.Errorf("plugin id is required")
	}
	if m.Version == "" {
		return fmt.Errorf("plugin version is required")
	}
	if m.ProtocolVersion <= 0 {
		return fmt.Errorf("plugin protocol_version must be a positive integer, got %d", m.ProtocolVersion)
	}
	if !m.Family.valid() {
		return fmt.Errorf("plugin family %q is not recognized", m.Family)
	}
	seen := make(map[string]bool, len(m.Capabilities))
	for _, capability := range m.Capabilities {
		if err := validateCapability(capability); err != nil {
			return fmt.Errorf("invalid capability %q: %w", capability, err)
		}
		if seen[capability] {
			return fmt.Errorf("duplicate capability %q", capability)
		}
		seen[capability] = true
	}
	return nil
}

// HasCapability reports whether the manifest declares it can perform
// capability. The core asks this - never a plugin-identity check - to
// decide whether to route an operation to a plugin at all.
func (m PluginManifest) HasCapability(capability string) bool {
	for _, c := range m.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

// SupportsProtocol reports whether the manifest's declared protocol
// version is one the host negotiating against it understands. Hosts
// should refuse to dispatch to a plugin whose protocol version they
// don't recognize rather than guess at compatibility - see Sanetizer-todo.md
// item 12 (capability negotiation): "il refuse explicitement les
// opérations incompatibles."
func (m PluginManifest) SupportsProtocol(hostSupported ...int) bool {
	for _, v := range hostSupported {
		if v == m.ProtocolVersion {
			return true
		}
	}
	return false
}

// validateCapability checks if a single capability string is valid.
// Used internally by Validate for duplicate detection.
func validateCapability(capability string) error {
	if capability == "" {
		return fmt.Errorf("capability must not be empty")
	}
	// Allow alphanumeric, dots, hyphens, underscores for flexibility
	// This is more permissive than api/plugin/manifest.go to support
	// the examples in Sanetizer-todo.md (e.g., "deployment.apply")
	for _, c := range capability {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '.' || c == '-' || c == '_') {
			return fmt.Errorf("capability must contain only alphanumeric, dots, hyphens, or underscores, got %q", capability)
		}
	}
	if len(capability) > 64 {
		return fmt.Errorf("capability must be at most 64 characters, got %d", len(capability))
	}
	return nil
}

// ValidateCapability checks if a capability string is valid for use in a manifest.
// This is exported for use by other packages that need to validate capabilities.
func ValidateCapability(capability string) error {
	return validateCapability(capability)
}

// IsValidCapability is a convenience function that returns true if the capability is valid.
func IsValidCapability(capability string) bool {
	return validateCapability(capability) == nil
}

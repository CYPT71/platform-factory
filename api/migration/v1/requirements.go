package v1

// Requirement represents a capability required by a resource
type Requirement struct {
	// Capability is the specific capability required (e.g., "discover", "apply")
	Capability string `json:"capability" yaml:"capability"`

	// Version is the version of the required capability
	Version string `json:"version" yaml:"version"`
}

package v1

import (
	"fmt"
	"strings"
)

const (
	MaxResources       = 100000
	MaxRequirements    = 1024
	MaxAttributes      = 4096
	MaxIdentifierBytes = 1024
)

// Resource represents a discoverable entity in the system
type Resource struct {
	// ID is the unique identifier for this resource within its source
	ID string `json:"id" yaml:"id"`

	// Kind identifies the type of resource (e.g., "vm", "disk", "network")
	Kind string `json:"kind" yaml:"kind"`

	// Source provides information about where this resource originated
	Source ResourceOrigin `json:"source" yaml:"source"`

	// Attributes contains key-value pairs with resource-specific metadata
	Attributes map[string]interface{} `json:"attributes,omitempty" yaml:"attributes,omitempty"`

	// Requirements specifies the capabilities needed to operate on this resource
	Requirements []Requirement `json:"requirements,omitempty" yaml:"requirements,omitempty"`
}

// ResourceOrigin describes where a resource came from
type ResourceOrigin struct {
	// PluginID identifies the plugin that discovered this resource
	PluginID string `json:"plugin_id" yaml:"plugin_id"`

	// NativeType is the native type of the resource as understood by the source plugin
	NativeType string `json:"native_type" yaml:"native_type"`

	// NativeID is the original identifier used by the source system
	NativeID string `json:"native_id" yaml:"native_id"`

	// Location provides additional contextual information about where the resource exists
	Location string `json:"location,omitempty" yaml:"location,omitempty"`
}

// Validate checks that a Resource has valid structure and data
func (r *Resource) Validate() error {
	if invalidText(r.ID) {
		return &ValidationError{"resource ID cannot be empty"}
	}
	if invalidText(r.Kind) {
		return &ValidationError{"resource kind cannot be empty"}
	}
	if invalidText(r.Source.PluginID) {
		return &ValidationError{"resource source plugin ID cannot be empty"}
	}
	if invalidText(r.Source.NativeType) {
		return &ValidationError{"resource source native type cannot be empty"}
	}
	if invalidText(r.Source.NativeID) {
		return &ValidationError{"resource source native ID cannot be empty"}
	}

	if len(r.Requirements) > MaxRequirements {
		return &ValidationError{"resource has too many requirements"}
	}
	if len(r.Attributes) > MaxAttributes {
		return &ValidationError{"resource has too many attributes"}
	}
	for key := range r.Attributes {
		if invalidText(key) {
			return &ValidationError{"resource attribute key is invalid"}
		}
		if secretKey(key) {
			return &ValidationError{"resource attributes must not contain secrets"}
		}
	}
	if err := validateAttributeValue(r.Attributes, 0, new(int)); err != nil {
		return err
	}

	for _, req := range r.Requirements {
		if err := req.Validate(); err != nil {
			return err
		}
	}

	return nil
}

const maxAttributeDepth = 32

func validateAttributeValue(value interface{}, depth int, nodes *int) error {
	if depth > maxAttributeDepth {
		return &ValidationError{"resource attributes exceed nesting limit"}
	}
	*nodes++
	if *nodes > MaxAttributes {
		return &ValidationError{"resource attributes exceed value limit"}
	}
	switch typed := value.(type) {
	case string:
		if secretValue(typed) {
			return &ValidationError{"resource attributes must not contain secret values"}
		}
		return nil
	case nil, bool, float64, float32,
		int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return nil
	case map[string]interface{}:
		for key, child := range typed {
			if invalidText(key) || secretKey(key) {
				return &ValidationError{"resource attributes must not contain secrets or invalid keys"}
			}
			if err := validateAttributeValue(child, depth+1, nodes); err != nil {
				return err
			}
		}
		return nil
	case []interface{}:
		for _, child := range typed {
			if err := validateAttributeValue(child, depth+1, nodes); err != nil {
				return err
			}
		}
		return nil
	default:
		return &ValidationError{fmt.Sprintf("resource attribute has unsupported type %T", value)}
	}
}

func secretValue(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	for _, marker := range []string{"secret-sentinel", "password=", "passwd=", "secret=", "access_token=", "api_key=", "private_key=", "-----begin private key", "-----begin rsa private key"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

// ValidateRequirement checks that a Requirement has valid structure and data
func (req *Requirement) Validate() error {
	if invalidText(req.Capability) {
		return &ValidationError{"requirement capability cannot be empty"}
	}
	if invalidText(req.Version) {
		return &ValidationError{"requirement version cannot be empty"}
	}
	return nil
}

func invalidText(s string) bool {
	return strings.TrimSpace(s) == "" || len(s) > MaxIdentifierBytes || strings.IndexByte(s, 0) >= 0
}

func secretKey(s string) bool {
	s = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, "-", "_"), ".", "_"))
	for _, marker := range []string{"password", "passwd", "secret", "private_key", "access_token", "api_key", "credential"} {
		if s == marker || strings.HasSuffix(s, "_"+marker) {
			return true
		}
	}
	return false
}

// ValidationError represents an error during validation
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return "migration validation error: " + e.Message
}

// IsValidationError checks if an error is a validation error
func IsValidationError(err error) bool {
	for err != nil {
		if _, ok := err.(*ValidationError); ok {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func duplicateError(kind, id string) error {
	return &ValidationError{Message: fmt.Sprintf("duplicate %s %q", kind, id)}
}

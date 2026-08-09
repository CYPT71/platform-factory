package migration

// DependencyEdge represents a relationship between two resources
type DependencyEdge struct {
	// From identifies the source resource in the dependency
	From ResourceID `json:"from" yaml:"from"`

	// To identifies the target resource in the dependency
	To ResourceID `json:"to" yaml:"to"`

	// Relation describes the nature of the dependency (e.g., "requires", "depends_on")
	Relation string `json:"relation" yaml:"relation"`

	// Required indicates if this dependency is mandatory for the operation
	Required bool `json:"required" yaml:"required"`
}

// ResourceID uniquely identifies a resource
type ResourceID struct {
	// PluginID identifies the plugin that owns the resource
	PluginID string `json:"plugin_id" yaml:"plugin_id"`

	// NativeType is the native type of the resource as understood by the source plugin
	NativeType string `json:"native_type" yaml:"native_type"`

	// NativeID is the original identifier used by the source system
	NativeID string `json:"native_id" yaml:"native_id"`
}

// Validate checks that a DependencyEdge has valid structure and data
func (e *DependencyEdge) Validate() error {
	if invalidText(e.From.PluginID) {
		return &ValidationError{"dependency edge from plugin ID cannot be empty"}
	}
	if invalidText(e.From.NativeType) {
		return &ValidationError{"dependency edge from native type cannot be empty"}
	}
	if invalidText(e.From.NativeID) {
		return &ValidationError{"dependency edge from native ID cannot be empty"}
	}
	if invalidText(e.To.PluginID) {
		return &ValidationError{"dependency edge to plugin ID cannot be empty"}
	}
	if invalidText(e.To.NativeType) {
		return &ValidationError{"dependency edge to native type cannot be empty"}
	}
	if invalidText(e.To.NativeID) {
		return &ValidationError{"dependency edge to native ID cannot be empty"}
	}
	if invalidText(e.Relation) {
		return &ValidationError{"dependency edge relation cannot be empty"}
	}

	return nil
}

// Validate checks that a ResourceID has valid structure and data
func (id *ResourceID) Validate() error {
	if invalidText(id.PluginID) {
		return &ValidationError{"resource ID plugin ID cannot be empty"}
	}
	if invalidText(id.NativeType) {
		return &ValidationError{"resource ID native type cannot be empty"}
	}
	if invalidText(id.NativeID) {
		return &ValidationError{"resource ID native ID cannot be empty"}
	}

	return nil
}

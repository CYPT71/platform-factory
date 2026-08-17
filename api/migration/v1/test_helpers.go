package v1

// This file contains test helpers for the migration package.

// TestResource creates a basic resource for testing
func TestResource(id, kind string) Resource {
	return Resource{
		ID:   id,
		Kind: kind,
		Source: ResourceOrigin{
			PluginID:   "test-plugin",
			NativeType: "test-type",
			NativeID:   id,
		},
		Attributes: map[string]interface{}{},
		Requirements: []Requirement{
			{
				Capability: "discover",
				Version:    "1.0.0",
			},
		},
	}
}

// TestDependencyEdge creates a basic dependency edge for testing
func TestDependencyEdge(from, to ResourceID, relation string) DependencyEdge {
	return DependencyEdge{
		From:     from,
		To:       to,
		Relation: relation,
		Required: true,
	}
}

// TestResourceID creates a basic resource ID for testing
func TestResourceID(pluginID, nativeType, nativeID string) ResourceID {
	return ResourceID{
		PluginID:   pluginID,
		NativeType: nativeType,
		NativeID:   nativeID,
	}
}

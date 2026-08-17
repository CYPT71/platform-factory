package v1

func testResource(id, kind string) Resource {
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

func testDependencyEdge(from, to ResourceID, relation string) DependencyEdge {
	return DependencyEdge{
		From:     from,
		To:       to,
		Relation: relation,
		Required: true,
	}
}

func testResourceID(pluginID, nativeType, nativeID string) ResourceID {
	return ResourceID{
		PluginID:   pluginID,
		NativeType: nativeType,
		NativeID:   nativeID,
	}
}

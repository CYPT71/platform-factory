package migration

import "testing"

func FuzzCanonicalGraph(f *testing.F) {
	f.Add("api", "database", "uses", true)
	f.Add("same", "same", "self", false)
	f.Fuzz(func(t *testing.T, from, to, relation string, required bool) {
		if len(from) > 128 || len(to) > 128 || len(relation) > 128 {
			t.Skip()
		}
		aggregate := Aggregate{Discovery: DiscoveryComplete, Resources: []Resource{
			{ID: from, Kind: "compute", Origin: ResourceOrigin{Source: "fuzz", NativeType: "unit", NativeID: "from"}, Requirements: []Requirement{{Capability: "migration.apply", Version: "v1"}}},
			{ID: to, Kind: "compute", Origin: ResourceOrigin{Source: "fuzz", NativeType: "unit", NativeID: "to"}, Requirements: []Requirement{{Capability: "migration.apply", Version: "v1"}}},
		}, Edges: []DependencyEdge{{From: from, To: to, Relation: relation, Required: required}}}
		plan, err := BuildPlan(aggregate)
		if err == nil {
			if err := plan.Validate(); err != nil {
				t.Fatalf("built invalid plan: %v", err)
			}
		}
	})
}

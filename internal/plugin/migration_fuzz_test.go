package plugin

import (
	"encoding/json"
	"testing"

	domainmigration "github.com/CYPT71/platform-factory/internal/migration"
)

func FuzzMigrationWireDiscovery(f *testing.F) {
	f.Add([]byte(`{"status":"complete","resources":[]}`))
	f.Add([]byte(`{"status":"partial","unknowns":[{"source":"p","kind":"denied","scope":"all","reason":"no"}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		var wire migrationDiscoverResult
		if json.Unmarshal(data, &wire) != nil {
			return
		}
		page := discoveryPageFromWire(wire)
		aggregate := domainmigration.Aggregate{Discovery: page.Status, Resources: page.Resources, Edges: page.Edges, Unknowns: page.Unknowns, Gaps: page.Gaps}
		_ = aggregate.Validate()
	})
}

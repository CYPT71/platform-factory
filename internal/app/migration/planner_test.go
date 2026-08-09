package migration

import (
	"context"
	"errors"
	"testing"

	domainmigration "github.com/CYPT71/secure-oci-base/internal/migration"
)

func TestPlannerBuildUsesDomainPlanner(t *testing.T) {
	input := domainmigration.Aggregate{Discovery: domainmigration.DiscoveryComplete, Resources: []domainmigration.Resource{{
		ID: "vm", Kind: "compute", Origin: domainmigration.ResourceOrigin{Source: "source", NativeType: "vm", NativeID: "native-vm"},
		Requirements: []domainmigration.Requirement{{Capability: "migration.apply", Version: "v1"}},
	}}}
	plan, err := NewPlanner().Build(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 || plan.Digest == "" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestPlannerBuildPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewPlanner().Build(ctx, domainmigration.Aggregate{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context cancellation", err)
	}
}

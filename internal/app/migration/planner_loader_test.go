package migration

import (
	"context"
	"errors"
	"testing"

	domainmigration "github.com/CYPT71/platform-factory/internal/migration"
)

type aggregateLoader struct {
	aggregate domainmigration.Aggregate
	err       error
}

func (l aggregateLoader) LoadCanonicalAggregate(context.Context) (domainmigration.Aggregate, error) {
	return l.aggregate, l.err
}

func TestPlannerLoadsRevalidatesAndRebuildsCanonicalPlan(t *testing.T) {
	aggregate := domainmigration.Aggregate{Discovery: domainmigration.DiscoveryComplete, Resources: []domainmigration.Resource{{ID: "app", Kind: "service", Origin: domainmigration.ResourceOrigin{Source: "dto", NativeType: "unit", NativeID: "app"}, Requirements: []domainmigration.Requirement{{Capability: "migration.apply", Version: "v1"}}}}}
	plan, err := NewPlanner().LoadAndBuild(context.Background(), aggregateLoader{aggregate: aggregate})
	if err != nil || len(plan.Steps) != 1 || plan.Digest == "" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	invalid := aggregate
	invalid.Resources[0].Attributes = map[string]string{"description": "password=sentinel"}
	if _, err := NewPlanner().LoadAndBuild(context.Background(), aggregateLoader{aggregate: invalid}); err == nil {
		t.Fatal("invalid loaded aggregate accepted")
	}
	if _, err := NewPlanner().LoadAndBuild(context.Background(), aggregateLoader{err: errors.New("corrupt DTO")}); err == nil {
		t.Fatal("loader failure hidden")
	}
	if _, err := NewPlanner().LoadAndBuild(context.Background(), nil); err == nil {
		t.Fatal("nil loader accepted")
	}
}

package migration

import (
	"context"
	"errors"
	"fmt"

	domainmigration "github.com/CYPT71/platform-factory/internal/migration"
)

// Planner is the application use case for deterministic domain planning.
// Outer adapters normalize transport or plugin observations before calling it.
type Planner struct{}

type CanonicalAggregateLoader interface {
	LoadCanonicalAggregate(context.Context) (domainmigration.Aggregate, error)
}

func NewPlanner() *Planner { return &Planner{} }

func (p *Planner) Build(ctx context.Context, input domainmigration.Aggregate) (domainmigration.Plan, error) {
	if err := ctx.Err(); err != nil {
		return domainmigration.Plan{}, err
	}
	plan, err := domainmigration.BuildPlan(input)
	if err != nil {
		return domainmigration.Plan{}, err
	}
	if err := ctx.Err(); err != nil {
		return domainmigration.Plan{}, err
	}
	return plan, nil
}

func (p *Planner) LoadAndBuild(ctx context.Context, loader CanonicalAggregateLoader) (domainmigration.Plan, error) {
	if err := ctx.Err(); err != nil {
		return domainmigration.Plan{}, err
	}
	if loader == nil {
		return domainmigration.Plan{}, errors.New("migration planner: canonical aggregate loader is required")
	}
	input, err := loader.LoadCanonicalAggregate(ctx)
	if err != nil {
		return domainmigration.Plan{}, fmt.Errorf("migration planner: load canonical aggregate: %w", err)
	}
	return p.Build(ctx, input)
}

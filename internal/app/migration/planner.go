package migration

import (
	"context"

	domainmigration "github.com/CYPT71/secure-oci-base/internal/migration"
)

// Planner is the application use case for deterministic domain planning.
// Outer adapters normalize transport or plugin observations before calling it.
type Planner struct{}

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

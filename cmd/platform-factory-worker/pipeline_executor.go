package main

import (
	"context"

	"github.com/CYPT71/platform-factory/internal/app/workerpipeline"
	"github.com/CYPT71/platform-factory/internal/cache"
)

const pipelineLeaseAPIVersion = workerpipeline.LeaseAPIVersion

type pipelineLeasePayload = workerpipeline.LeasePayload

type leaseBlob = workerpipeline.LeaseBlob

func pipelineLeaseExecutor(root string, store cache.ContentStore, pull func(context.Context, cache.Descriptor) error) (func(context.Context, Lease) (string, error), error) {
	execute, err := workerpipeline.Executor(root, store, pull)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, lease Lease) (string, error) {
		return execute(ctx, lease.Payload)
	}, nil
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/CYPT71/platform-factory/internal/cache"
	"github.com/CYPT71/platform-factory/internal/executor"
	"github.com/CYPT71/platform-factory/internal/pipeline"
	"github.com/CYPT71/platform-factory/internal/strictjson"
)

const pipelineLeaseAPIVersion = "platform-factory.dev/worker-pipeline/v1"

type pipelineLeasePayload struct {
	APIVersion string          `json:"api_version"`
	Workdir    string          `json:"workdir"`
	Pipeline   json.RawMessage `json:"pipeline"`
	Blobs      []leaseBlob     `json:"blobs,omitempty"`
}

type leaseBlob struct {
	cache.Descriptor
	Target string `json:"target"`
}

func pipelineLeaseExecutor(root string, store cache.ContentStore, pull func(context.Context, cache.Descriptor) error) (func(context.Context, Lease) (string, error), error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, fmt.Errorf("worker: execution root must be a real directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("worker: execution root must be a real directory, not a symlink")
	}
	return func(ctx context.Context, lease Lease) (string, error) {
		if len(lease.Payload) > maxResponseBodyBytes {
			return "", errors.New("worker: pipeline lease exceeds 1 MiB")
		}
		var payload pipelineLeasePayload
		if err := strictjson.Decode([]byte(lease.Payload), &payload); err != nil {
			return "", fmt.Errorf("worker: decode pipeline lease: %w", err)
		}
		if payload.APIVersion != pipelineLeaseAPIVersion {
			return "", fmt.Errorf("worker: unsupported pipeline lease API version %q", payload.APIVersion)
		}
		if payload.Workdir == "" || filepath.IsAbs(payload.Workdir) || filepath.Clean(payload.Workdir) != payload.Workdir || payload.Workdir == "." || filepath.Clean(payload.Workdir) == ".." {
			return "", errors.New("worker: workdir must be a clean relative path")
		}
		workdir := filepath.Join(absolute, payload.Workdir)
		if err := os.Mkdir(workdir, 0o700); err != nil {
			return "", fmt.Errorf("worker: create workdir: %w", err)
		}
		for _, blob := range payload.Blobs {
			if store == nil || pull == nil {
				return "", errors.New("worker: pipeline lease requires an unconfigured CAS")
			}
			if blob.Target == "" || filepath.IsAbs(blob.Target) || filepath.Clean(blob.Target) != blob.Target || blob.Target == "." || filepath.Clean(blob.Target) == ".." {
				return "", errors.New("worker: blob target must be a clean relative path")
			}
			if err := pull(ctx, blob.Descriptor); err != nil {
				return "", fmt.Errorf("worker: pull blob %s: %w", blob.Digest, err)
			}
			reader, err := store.Get(blob.Digest)
			if err != nil {
				return "", err
			}
			target := filepath.Join(workdir, blob.Target)
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				reader.Close()
				return "", err
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err == nil {
				_, err = io.Copy(file, io.LimitReader(reader, blob.Size+1))
			}
			closeErr, readCloseErr := error(nil), reader.Close()
			if file != nil {
				closeErr = file.Close()
			}
			if err != nil || closeErr != nil || readCloseErr != nil {
				return "", errors.Join(err, closeErr, readCloseErr)
			}
		}
		definition, _, err := pipeline.Decode(bytes.NewReader(payload.Pipeline))
		if err != nil {
			return "", fmt.Errorf("worker: decode pipeline: %w", err)
		}
		runner := executor.New(workdir, nil)
		report, err := (pipeline.Scheduler{Parallelism: 1, Runner: runner}).Run(ctx, definition)
		if err != nil {
			return "", err
		}
		result, err := json.Marshal(report)
		return string(result), err
	}, nil
}

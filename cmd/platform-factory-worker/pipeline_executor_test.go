package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/cache"
)

func TestPipelineLeaseExecutorRunsARealStage(t *testing.T) {
	root := t.TempDir()
	execute, err := pipelineLeaseExecutor(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pipeline := json.RawMessage(`{"api_version":"platform-factory.dev/v1","name":"real-worker","stages":[{"id":"build","command":{"executable":"sh","args":["-c","printf built > artifact"]},"network":"none"}]}`)
	payload, _ := json.Marshal(pipelineLeasePayload{APIVersion: pipelineLeaseAPIVersion, Workdir: "lease-1", Pipeline: pipeline})
	result, err := execute(context.Background(), Lease{ID: "lease-1", Payload: string(payload)})
	if err != nil || !strings.Contains(result, "succeeded") {
		t.Fatalf("result=%q err=%v", result, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "lease-1", "artifact")); err != nil || string(data) != "built" {
		t.Fatalf("artifact=%q err=%v", data, err)
	}
}

func TestPipelineLeaseExecutorRejectsHostilePayloads(t *testing.T) {
	root := t.TempDir()
	execute, err := pipelineLeaseExecutor(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string]string{
		"unknown envelope": `{"api_version":"platform-factory.dev/worker-pipeline/v1","workdir":"x","pipeline":{},"extra":true}`,
		"future version":   `{"api_version":"v2","workdir":"x","pipeline":{}}`,
		"absolute path":    `{"api_version":"platform-factory.dev/worker-pipeline/v1","workdir":"/tmp/x","pipeline":{}}`,
		"traversal":        `{"api_version":"platform-factory.dev/worker-pipeline/v1","workdir":"../x","pipeline":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := execute(context.Background(), Lease{Payload: payload}); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "collision")); err != nil {
		t.Fatal(err)
	}
	pipeline := `{"api_version":"platform-factory.dev/v1","name":"x","stages":[]}`
	payload := `{"api_version":"platform-factory.dev/worker-pipeline/v1","workdir":"collision","pipeline":` + pipeline + `}`
	if _, err := execute(context.Background(), Lease{Payload: payload}); err == nil {
		t.Fatal("expected existing workspace/symlink rejection")
	}
}

func TestPipelineLeaseExecutorPullsVerifiedCASInput(t *testing.T) {
	source, _ := cache.Open(t.TempDir())
	destination, _ := cache.Open(t.TempDir())
	descriptor, _ := source.Put(strings.NewReader("from-cas"))
	server := httptest.NewServer(cache.BlobHandler(source, 1024))
	defer server.Close()
	root := t.TempDir()
	execute, err := pipelineLeaseExecutor(root, destination, func(ctx context.Context, descriptor cache.Descriptor) error {
		return cache.PullBlob(ctx, server.Client(), server.URL, destination, descriptor, 1024)
	})
	if err != nil {
		t.Fatal(err)
	}
	pipeline := json.RawMessage(`{"api_version":"platform-factory.dev/v1","name":"cas-worker","stages":[{"id":"build","command":{"executable":"sh","args":["-c","cat input.txt > artifact"]},"network":"none"}]}`)
	payload, _ := json.Marshal(pipelineLeasePayload{APIVersion: pipelineLeaseAPIVersion, Workdir: "lease-cas", Pipeline: pipeline,
		Blobs: []leaseBlob{{Descriptor: descriptor, Target: "input.txt"}}})
	if _, err := execute(context.Background(), Lease{Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "lease-cas", "artifact")); err != nil || string(data) != "from-cas" {
		t.Fatalf("artifact=%q err=%v", data, err)
	}
}

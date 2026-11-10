package workerpipeline

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/cache"
	"github.com/CYPT71/platform-factory/internal/core"
)

func TestExecutorRunsARealStage(t *testing.T) {
	root := t.TempDir()
	execute, err := Executor(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pipeline := json.RawMessage(`{"api_version":"platform-factory.dev/v1","name":"real-worker","stages":[{"id":"build","command":{"executable":"sh","args":["-c","printf built > artifact"]},"network":"none"}]}`)
	payload, _ := json.Marshal(LeasePayload{APIVersion: LeaseAPIVersion, Workdir: "lease-1", Pipeline: pipeline})
	result, err := execute(context.Background(), string(payload))
	if err != nil || !strings.Contains(result, "succeeded") {
		t.Fatalf("result=%q err=%v", result, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "lease-1", "artifact")); err != nil || string(data) != "built" {
		t.Fatalf("artifact=%q err=%v", data, err)
	}
}

func TestExecutorRejectsParentTraversalBeforeMaterialization(t *testing.T) {
	for _, tc := range []struct {
		name, workdir, target string
	}{
		{"workdir parent", "../escaped", ""},
		{"workdir ancestor", "../../escaped", ""},
		{"blob parent", "lease", "../escaped"},
		{"blob ancestor", "lease", "../../escaped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "parent", "root")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			store, err := cache.Open(filepath.Join(base, "cas"))
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err := store.Put(strings.NewReader("must stay in the workspace"))
			if err != nil {
				t.Fatal(err)
			}
			pulls := 0
			execute, err := Executor(root, store, func(context.Context, cache.Descriptor) error {
				pulls++
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			payload := LeasePayload{APIVersion: LeaseAPIVersion, Workdir: tc.workdir,
				Pipeline: json.RawMessage(`{"api_version":"platform-factory.dev/v1","name":"bounded","stages":[{"id":"build","command":{"executable":"sh","args":["-c","printf built > artifact"]},"network":"none"}]}`)}
			outside := filepath.Join(root, tc.workdir)
			if tc.target != "" {
				payload.Blobs = []LeaseBlob{{Descriptor: descriptor, Target: tc.target}}
				outside = filepath.Join(outside, tc.target)
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := execute(context.Background(), string(raw)); err == nil || !strings.Contains(err.Error(), "clean relative path") {
				t.Errorf("traversing path must be rejected before execution: %v", err)
			}
			if pulls != 0 {
				t.Errorf("invalid path triggered %d CAS fetches", pulls)
			}
			if _, err := os.Lstat(outside); !os.IsNotExist(err) {
				t.Errorf("traversal created an outside path: %s (%v)", outside, err)
			}
		})
	}
}

func TestExecutorRejectsEscapingWorkspaceSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "redirect")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	execute, err := Executor(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"api_version":"platform-factory.dev/worker-pipeline/v1","workdir":"redirect/lease","pipeline":{"api_version":"platform-factory.dev/v1","name":"bounded","stages":[{"id":"build","command":{"executable":"sh","args":["-c","printf built > artifact"]},"network":"none"}]}}`
	if _, err := execute(context.Background(), payload); err == nil {
		t.Error("workspace creation followed a symlink outside the root")
	}
	if _, err := os.Lstat(filepath.Join(outside, "lease")); !os.IsNotExist(err) {
		t.Errorf("outside workspace was created: %v", err)
	}
}

func TestExecutorRejectsEscapingBlobSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	store, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := store.Put(strings.NewReader("must stay in the workspace"))
	if err != nil {
		t.Fatal(err)
	}
	execute, err := Executor(root, store, func(context.Context, cache.Descriptor) error {
		if err := os.Symlink(outside, filepath.Join(root, "lease", "redirect")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(LeasePayload{APIVersion: LeaseAPIVersion, Workdir: "lease",
		Pipeline: json.RawMessage(`{"api_version":"platform-factory.dev/v1","name":"bounded","stages":[{"id":"build","command":{"executable":"sh","args":["-c","printf built > artifact"]},"network":"none"}]}`),
		Blobs:    []LeaseBlob{{Descriptor: descriptor, Target: "redirect/escaped"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execute(context.Background(), string(payload)); err == nil {
		t.Error("blob materialization followed a symlink outside the workspace")
	}
	if _, err := os.Lstat(filepath.Join(outside, "escaped")); !os.IsNotExist(err) {
		t.Errorf("outside blob was created: %v", err)
	}
}

func TestExecutorRejectsHostilePayloads(t *testing.T) {
	root := t.TempDir()
	execute, err := Executor(root, nil, nil)
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
			if _, err := execute(context.Background(), payload); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "collision")); err != nil {
		t.Fatal(err)
	}
	pipeline := `{"api_version":"platform-factory.dev/v1","name":"x","stages":[]}`
	payload := `{"api_version":"platform-factory.dev/worker-pipeline/v1","workdir":"collision","pipeline":` + pipeline + `}`
	if _, err := execute(context.Background(), payload); err == nil {
		t.Fatal("expected existing workspace/symlink rejection")
	}
}

func TestExecutorPullsVerifiedCASInput(t *testing.T) {
	source, _ := cache.Open(t.TempDir())
	destination, _ := cache.Open(t.TempDir())
	descriptor, _ := source.Put(strings.NewReader("from-cas"))
	server := httptest.NewServer(cache.BlobHandler(source, 1024))
	defer server.Close()
	root := t.TempDir()
	execute, err := Executor(root, destination, func(ctx context.Context, descriptor cache.Descriptor) error {
		return cache.PullBlob(ctx, server.Client(), server.URL, destination, descriptor, 1024)
	})
	if err != nil {
		t.Fatal(err)
	}
	pipeline := json.RawMessage(`{"api_version":"platform-factory.dev/v1","name":"cas-worker","stages":[{"id":"build","command":{"executable":"sh","args":["-c","cat src/input.txt > artifact"]},"network":"none"}]}`)
	payload, _ := json.Marshal(LeasePayload{APIVersion: LeaseAPIVersion, Workdir: "lease-cas", Pipeline: pipeline,
		Blobs: []LeaseBlob{{Descriptor: descriptor, Target: "src/input.txt"}}})
	if _, err := execute(context.Background(), string(payload)); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "lease-cas", "artifact")); err != nil || string(data) != "from-cas" {
		t.Fatalf("artifact=%q err=%v", data, err)
	}
}

// An unsupported later stage must not allow earlier commands or CAS fetches.
func TestExecutorPreflightsEveryStageBeforeMaterialization(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage core.Stage
	}{
		{"network", core.Stage{Network: core.NetworkFull}},
		{"cpu", core.Stage{Resources: core.ResourceLimits{CPUMilli: 100}}},
		{"pids", core.Stage{Resources: core.ResourceLimits{PIDs: 10}}},
		{"secrets", core.Stage{Secrets: []core.SecretReference{{ID: "token", Target: "/run/token"}}}},
		{"read-only", core.Stage{Sandbox: core.SandboxPolicy{ReadOnlyRoot: true}}},
		{"non-root", core.Stage{Sandbox: core.SandboxPolicy{NonRoot: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := cache.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err := store.Put(strings.NewReader("input"))
			if err != nil {
				t.Fatal(err)
			}
			pulls := 0
			execute, err := Executor(root, store, func(context.Context, cache.Descriptor) error { pulls++; return nil })
			if err != nil {
				t.Fatal(err)
			}
			stage := tc.stage
			stage.ID = "unsupported"
			stage.DependsOn = []string{"first"}
			if stage.Network == core.NetworkFull {
				stage.DependsOn = nil // Network-enabled stages must be DAG roots.
			}
			stage.Command.Executable = "sh"
			definition, err := json.Marshal(core.Pipeline{APIVersion: core.PipelineAPIVersion, Name: "preflight", Stages: []core.Stage{
				{ID: "first", Command: core.Command{Executable: "sh", Args: []string{"-c", "printf changed > marker"}}}, stage,
			}})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(LeasePayload{APIVersion: LeaseAPIVersion, Workdir: "lease", Pipeline: definition, Blobs: []LeaseBlob{{Descriptor: descriptor, Target: "input"}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := execute(context.Background(), string(raw)); err == nil || !strings.Contains(err.Error(), "unsupported") {
				t.Fatalf("expected unsupported-stage error, got %v", err)
			}
			if pulls != 0 {
				t.Errorf("unsupported pipeline fetched %d blobs", pulls)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("unsupported pipeline modified execution root: %v", entries)
			}
		})
	}
}

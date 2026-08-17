package conformance

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"

	api "github.com/CYPT71/platform-factory/api/pipeline/v1alpha1"
	"github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/executor"
)

//go:embed vectors-backend/*.json
var embeddedBackendVectors embed.FS

// EmbeddedBackendVectors returns the execution-backend vector corpus
// compiled into this build.
func EmbeddedBackendVectors() fs.FS { return embeddedBackendVectors }

// BackendVector pins the expected outcome of running one stage in
// isolation - no dependency on any other stage, cache, or DAG - against
// an execution backend.
type BackendVector struct {
	Name   string        `json:"name"`
	Stage  api.Stage     `json:"stage"`
	Expect BackendExpect `json:"expect"`
}

// BackendExpect describes the pinned outcome. ExitCode -1 is the
// internal/executor convention for a stage rejected before it ever ran
// (an unsupported policy, for example), distinct from a stage that ran
// and exited non-zero.
type BackendExpect struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
}

// RunBackend evaluates every vector in fsys against a fresh local
// (unsandboxed) internal/executor.Executor, one per vector so no vector
// can observe another's filesystem or environment. This is the
// conformance baseline every execution backend must satisfy - the
// sandboxed backend adds isolation on top without changing these
// observable outcomes, and is not itself exercised here because its
// namespace and cgroup requirements are not available on every host
// (see internal/executor.ProbeSandbox). The vectors assume a POSIX
// shell at /bin/sh.
func RunBackend(fsys fs.FS) ([]Result, error) {
	names, err := fs.Glob(fsys, "vectors-backend/*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, errors.New("conformance: no backend vectors found")
	}
	var results []Result
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		var vector BackendVector
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&vector); err != nil {
			return nil, fmt.Errorf("conformance: %s: %w", name, err)
		}
		results = append(results, evaluateBackendVector(vector))
	}
	return results, nil
}

func evaluateBackendVector(vector BackendVector) Result {
	dir, err := os.MkdirTemp("", "platform-factory-conformance-backend-*")
	if err != nil {
		return Result{Name: vector.Name, Passed: false, Detail: err.Error()}
	}
	defer os.RemoveAll(dir)

	backend := executor.New(dir, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stage, conversionErr := backendStage(vector.Stage)
	if conversionErr != nil {
		return Result{Name: vector.Name, Passed: false, Detail: conversionErr.Error()}
	}
	runErr := backend.Run(ctx, stage)
	recorded := backend.Results()
	if len(recorded) != 1 {
		return Result{Name: vector.Name, Passed: false,
			Detail: fmt.Sprintf("expected exactly one recorded result, got %d (run err=%v)", len(recorded), runErr)}
	}
	actual := BackendExpect{ExitCode: recorded[0].ExitCode, Stdout: string(recorded[0].Stdout)}
	if actual != vector.Expect {
		return Result{Name: vector.Name, Passed: false,
			Detail: fmt.Sprintf("expected %+v, got %+v (run err=%v)", vector.Expect, actual, runErr)}
	}
	return Result{Name: vector.Name, Passed: true}
}

func backendStage(source any) (core.Stage, error) {
	data, err := json.Marshal(source)
	if err != nil {
		return core.Stage{}, fmt.Errorf("conformance: encode backend stage: %w", err)
	}
	var stage core.Stage
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stage); err != nil {
		return core.Stage{}, fmt.Errorf("conformance: convert backend stage: %w", err)
	}
	return stage, nil
}

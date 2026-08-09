package pipeline

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	api "github.com/CYPT71/secure-oci-base/internal/core"
)

func TestAnalyzeAcceptsStableV1WireVersion(t *testing.T) {
	definition := validPipeline()
	definition.APIVersion = api.PipelineAPIVersion
	if _, err := Analyze(definition); err != nil {
		t.Fatalf("v1 rejected: %v", err)
	}
}

func TestAnalyzeAcceptsPromotedV1Beta1WireVersion(t *testing.T) {
	definition := validPipeline()
	definition.APIVersion = api.PipelineBetaAPIVersion
	if _, err := Analyze(definition); err != nil {
		t.Fatalf("v1beta1 rejected: %v", err)
	}
}

func TestAnalyzeForbidsNetworkAfterResolutionPhase(t *testing.T) {
	definition := validPipeline()
	definition.Stages[0].Network = api.NetworkFull
	if _, err := Analyze(definition); err == nil || !strings.Contains(err.Error(), "network-enabled resolution stages must be DAG roots") {
		t.Fatalf("err=%v", err)
	}
}

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func validPipeline() api.Pipeline {
	return api.Pipeline{
		APIVersion: api.APIVersion,
		Name:       "example",
		Inputs: []api.Input{{
			ID: "source", Kind: "directory", Source: ".", Digest: testDigest,
		}},
		Stages: []api.Stage{
			{
				ID:        "package",
				Command:   api.Command{Executable: "/bin/package", WorkingDir: "/workspace"},
				DependsOn: []string{"compile", "assets"},
				Inputs:    []api.ArtifactReference{{Stage: "compile", Name: "binary"}},
				Outputs:   []api.ArtifactDeclaration{{Name: "image", Path: "/out/image"}},
				Network:   api.NetworkNone,
				Sandbox:   api.SandboxPolicy{ReadOnlyRoot: true, NonRoot: true},
			},
			{
				ID:      "assets",
				Command: api.Command{Executable: "/bin/assets"},
				Outputs: []api.ArtifactDeclaration{{Name: "static", Path: "/out/static"}},
			},
			{
				ID:      "compile",
				Command: api.Command{Executable: "/bin/compiler", Args: []string{"build"}},
				Base: &api.ImageReference{
					Reference: "registry.example/toolchain", Digest: testDigest, Platform: "linux/amd64",
				},
				Env:       map[string]string{"LANG": "C"},
				Mounts:    []api.Mount{{Source: "source", Target: "/workspace", ReadOnly: true}},
				Secrets:   []api.SecretReference{{ID: "registry-token", Target: "/run/secrets/token"}},
				Caches:    []api.CacheMount{{ID: "compiler", Target: "/cache"}},
				Outputs:   []api.ArtifactDeclaration{{Name: "binary", Path: "/out/service"}},
				Network:   api.NetworkResolve,
				Resources: api.ResourceLimits{CPUMilli: 1000, MemoryMiB: 1024, PIDs: 128},
			},
		},
		Outputs: []api.Output{{Name: "image", Stage: "package", Artifact: "image"}},
	}
}

func TestAnalyzeReturnsDeterministicParallelLevels(t *testing.T) {
	definition := validPipeline()
	graph, err := Analyze(definition)
	if err != nil {
		t.Fatal(err)
	}
	wantLevels := [][]string{{"assets", "compile"}, {"package"}}
	wantOrder := []string{"assets", "compile", "package"}
	if !reflect.DeepEqual(graph.Levels, wantLevels) || !reflect.DeepEqual(graph.Order, wantOrder) {
		t.Fatalf("graph=%+v", graph)
	}
	definition.Stages[0], definition.Stages[2] = definition.Stages[2], definition.Stages[0]
	again, err := Analyze(definition)
	if err != nil || !reflect.DeepEqual(graph, again) {
		t.Fatalf("graph changed after input reorder: %+v err=%v", again, err)
	}
}

func TestAnalyzeRejectsCyclesDeterministically(t *testing.T) {
	definition := validPipeline()
	definition.Stages[1].DependsOn = []string{"package"}
	_, err := Analyze(definition)
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(err.Error(), "dependency cycle contains assets, package") {
		t.Fatalf("err=%v issues=%v", err, validation.Issues)
	}
}

func TestAnalyzeCollectsStrictValidationIssues(t *testing.T) {
	definition := api.Pipeline{
		APIVersion: "other/v1",
		Name:       "Bad_Name",
		Inputs: []api.Input{
			{ID: "Bad", Source: "\x00", Digest: "sha256:bad"},
			{ID: "source", Kind: "directory", Source: "."},
			{ID: "source", Kind: "directory", Source: "."},
		},
		Stages: []api.Stage{
			{
				ID:        "build",
				DependsOn: []string{"build", "missing", "missing"},
				Command: api.Command{
					Executable: "", Args: []string{"bad\x00"}, WorkingDir: "relative",
				},
				Base: &api.ImageReference{Reference: "", Digest: "bad", Platform: "windows/amd64"},
				Env: map[string]string{
					"": "value", "GOOD": "bad\x00", "PLATFORM_FACTORY_ROOT": "/attacker",
				},
				Mounts: []api.Mount{
					{Source: "", Target: "relative"},
					{Source: "other", Target: "/same"},
					{Source: "third", Target: "/same"},
				},
				Secrets: []api.SecretReference{{ID: "", Target: "/"}},
				Caches:  []api.CacheMount{{ID: "bad\x00", Target: "/cache/../cache"}},
				Inputs: []api.ArtifactReference{
					{Stage: "missing", Name: "none"},
					{Stage: "producer", Name: "missing"},
				},
				Outputs: []api.ArtifactDeclaration{
					{Name: "Bad", Path: "relative"},
					{Name: "artifact", Path: "/out"},
					{Name: "artifact", Path: "/other"},
				},
				Network:   "host",
				Resources: api.ResourceLimits{CPUMilli: -1},
			},
			{
				ID:      "build",
				Command: api.Command{Executable: "/bin/true"},
			},
			{
				ID:      "producer",
				Command: api.Command{Executable: "/bin/true"},
			},
		},
		Outputs: []api.Output{
			{Name: "Bad", Stage: "missing", Artifact: "none"},
			{Name: "result", Stage: "producer", Artifact: "missing"},
			{Name: "result", Stage: "build", Artifact: "none"},
		},
	}
	_, err := Analyze(definition)
	var validation *ValidationError
	if !errors.As(err, &validation) || len(validation.Issues) < 25 {
		t.Fatalf("err=%v issues=%v", err, validation.Issues)
	}
	for index := 1; index < len(validation.Issues); index++ {
		previous, current := validation.Issues[index-1], validation.Issues[index]
		if previous.Path > current.Path ||
			(previous.Path == current.Path && previous.Message > current.Message) {
			t.Fatalf("issues are not sorted at %d: %v then %v", index, previous, current)
		}
	}
}

func TestAnalyzeRejectsEmptyAndOversizedPipelines(t *testing.T) {
	for name, definition := range map[string]api.Pipeline{
		"empty": {APIVersion: api.APIVersion, Name: "empty"},
		"oversized": {
			APIVersion: api.APIVersion, Name: "large",
			Stages: make([]api.Stage, maxStages+1),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Analyze(definition); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidationErrorFallbackAndDigestValidation(t *testing.T) {
	if got := (&ValidationError{}).Error(); got != "invalid pipeline" {
		t.Fatalf("error=%q", got)
	}
	for value, valid := range map[string]bool{
		testDigest: true,
		"":         false,
		"sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA": false,
		"sha512:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": false,
	} {
		if got := validDigest(value); got != valid {
			t.Fatalf("validDigest(%q)=%v", value, got)
		}
	}
}

func TestAnalyzeValidatesRequiredCapabilities(t *testing.T) {
	definition := api.Pipeline{
		APIVersion: api.APIVersion, Name: "capable",
		RequiredCapabilities: []string{"cache", "parallel-stages"},
		Stages:               []api.Stage{{ID: "build", Command: api.Command{Executable: "/usr/bin/true"}}},
	}
	if _, err := Analyze(definition); err != nil {
		t.Fatalf("valid capabilities rejected: %v", err)
	}
	definition.RequiredCapabilities = []string{"time-travel"}
	if _, err := Analyze(definition); err == nil || !strings.Contains(err.Error(), "unknown capability") {
		t.Fatalf("err=%v", err)
	}
	definition.RequiredCapabilities = []string{"cache", "cache"}
	if _, err := Analyze(definition); err == nil || !strings.Contains(err.Error(), "duplicates capability") {
		t.Fatalf("err=%v", err)
	}
	if names := KnownCapabilities(); len(names) == 0 || !sort.StringsAreSorted(names) {
		t.Fatalf("names=%v", names)
	}
}

package scheduler

import (
	"reflect"
	"strings"
	"testing"

	api "github.com/CYPT71/platform-factory/internal/core"
)

const validSHA256 = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func validPipeline() api.Pipeline {
	return api.Pipeline{
		APIVersion: api.APIVersion,
		Name:       "pipeline",
		RequiredCapabilities: []string{
			CapabilityArtifacts,
			CapabilityCache,
		},
		Inputs: []api.Input{{ID: "source", Kind: "git", Source: "repo", Digest: validSHA256}},
		Stages: []api.Stage{
			{
				ID:        "build",
				Base:      &api.ImageReference{Reference: "example/base", Digest: validSHA256, Platform: "linux/amd64"},
				Command:   api.Command{Executable: "/bin/build", Args: []string{"--release"}, WorkingDir: "/workspace"},
				Env:       map[string]string{"MODE": "release"},
				Mounts:    []api.Mount{{Source: "source", Target: "/src", ReadOnly: true}},
				Secrets:   []api.SecretReference{{ID: "signing-key", Target: "/run/secrets/key"}},
				Caches:    []api.CacheMount{{ID: "compiler", Target: "/cache"}},
				Outputs:   []api.ArtifactDeclaration{{Name: "binary", Path: "/out/binary"}},
				Network:   api.NetworkResolve,
				Resources: api.ResourceLimits{CPUMilli: 1000, MemoryMiB: 512, PIDs: 64},
			},
			{
				ID:        "package",
				DependsOn: []string{"build"},
				Command:   api.Command{Executable: "/bin/package"},
				Inputs:    []api.ArtifactReference{{Stage: "build", Name: "binary", Target: "/inputs/binary"}},
				Outputs:   []api.ArtifactDeclaration{{Name: "image", Path: "/out/image"}},
			},
		},
		Outputs: []api.Output{{Name: "release", Stage: "package", Artifact: "image"}},
	}
}

func TestAnalyzeAcceptsCompletePipelineAndIsDeterministic(t *testing.T) {
	pipeline := validPipeline()
	want := Graph{Order: []string{"build", "package"}, Levels: [][]string{{"build"}, {"package"}}}
	for i := 0; i < 20; i++ {
		got, err := Analyze(pipeline)
		if err != nil {
			t.Fatalf("Analyze() error = %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Analyze() = %#v, want %#v", got, want)
		}
	}
}

func TestCapabilitiesAreSortedAndStrict(t *testing.T) {
	got := KnownCapabilities()
	if !reflect.DeepEqual(got, []string{
		CapabilityArtifacts, CapabilityCache, CapabilityCgroupCPU, CapabilityCgroupPIDs,
		CapabilityMemoryRlimit, CapabilityNetworkNone, CapabilityParallelStages,
		CapabilitySandbox, CapabilitySecrets,
	}) {
		t.Fatalf("KnownCapabilities() = %v", got)
	}
	issues := ValidateRequiredCapabilities([]string{CapabilityCache, "future", CapabilityCache})
	if len(issues) != 2 || issues[0].Path != "required_capabilities[1]" || issues[1].Path != "required_capabilities[2]" {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestAnalyzeReportsMalformedPipelineDeterministically(t *testing.T) {
	pipeline := validPipeline()
	pipeline.APIVersion = "invalid/v9"
	pipeline.Name = "Invalid"
	pipeline.RequiredCapabilities = []string{"unknown", "unknown"}
	pipeline.Inputs = append(pipeline.Inputs,
		api.Input{ID: "source", Kind: "", Source: "bad\x00", Digest: "sha256:xyz"},
		api.Input{ID: "Bad", Kind: "git", Source: "repo", Digest: validSHA256},
	)
	pipeline.Stages[0].ID = "Bad"
	pipeline.Stages[0].Command = api.Command{Executable: "bad\x00", Args: []string{"bad\x00"}, WorkingDir: "relative"}
	pipeline.Stages[0].Env = map[string]string{"": "bad\x00", "PLATFORM_FACTORY_ROOT": "/tmp"}
	pipeline.Stages[0].Network = "host"
	pipeline.Stages[0].Resources = api.ResourceLimits{CPUMilli: -1, MemoryMiB: -1, PIDs: -1}
	pipeline.Stages[0].Base = &api.ImageReference{Platform: "windows/amd64"}
	pipeline.Stages[0].Mounts = []api.Mount{{Source: "missing", Target: "relative"}}
	pipeline.Stages[0].Secrets = []api.SecretReference{{Target: "/"}}
	pipeline.Stages[0].Caches = []api.CacheMount{{ID: "cache", Target: "/cache"}, {ID: "other", Target: "/cache"}}
	pipeline.Stages[0].Outputs = []api.ArtifactDeclaration{{Name: "Bad", Path: "relative"}, {Name: "Bad", Path: "/out"}}
	pipeline.Stages[1].DependsOn = []string{"package", "missing", "missing"}
	pipeline.Stages[1].Inputs = []api.ArtifactReference{{Stage: "missing", Name: "none"}, {Stage: "Bad", Name: "none", Target: "relative"}}
	pipeline.Outputs = []api.Output{{Name: "Bad", Stage: "missing"}, {Name: "Bad", Stage: "package", Artifact: "none"}}

	_, err := Analyze(pipeline)
	var validation *ValidationError
	if err == nil || !strings.Contains(err.Error(), "api_version") {
		t.Fatalf("Analyze() error = %v", err)
	}
	if !errorAs(err, &validation) || len(validation.Issues) < 20 {
		t.Fatalf("validation issues = %#v", validation)
	}
	for i := 1; i < len(validation.Issues); i++ {
		before, after := validation.Issues[i-1], validation.Issues[i]
		if before.Path > after.Path || before.Path == after.Path && before.Message > after.Message {
			t.Fatalf("issues not sorted at %d: %#v then %#v", i, before, after)
		}
	}
}

func TestAnalyzeRejectsCyclesAndStageLimit(t *testing.T) {
	cycle := api.Pipeline{APIVersion: api.APIVersion, Name: "cycle", Stages: []api.Stage{
		{ID: "a", DependsOn: []string{"b"}, Command: api.Command{Executable: "/bin/a"}},
		{ID: "b", DependsOn: []string{"a"}, Command: api.Command{Executable: "/bin/b"}},
	}}
	_, err := Analyze(cycle)
	if err == nil || !strings.Contains(err.Error(), "dependency cycle contains a, b") {
		t.Fatalf("cycle error = %v", err)
	}

	tooLarge := api.Pipeline{APIVersion: api.APIVersion, Name: "large", Stages: make([]api.Stage, MaxStages+1)}
	_, err = Analyze(tooLarge)
	if err == nil || !strings.Contains(err.Error(), "10000 stage limit") {
		t.Fatalf("limit error = %v", err)
	}
}

func TestDigestAndPathValidation(t *testing.T) {
	for value, want := range map[string]bool{
		validSHA256:                         true,
		"sha256:" + strings.Repeat("A", 64): false,
		"sha512:" + strings.Repeat("a", 64): false,
		"sha256:short":                      false,
	} {
		if got := ValidDigest(value); got != want {
			t.Errorf("ValidDigest(%q) = %v, want %v", value, got, want)
		}
	}
	for value, want := range map[string]bool{"/valid/path": true, "": false, "/": false, "relative": false, "/a/../b": false} {
		if got := cleanAbsolutePath(value); got != want {
			t.Errorf("cleanAbsolutePath(%q) = %v, want %v", value, got, want)
		}
	}
}

// errorAs is local to keep this validation test focused without shadowing the
// scheduler's public error behavior with string parsing.
func errorAs(err error, target **ValidationError) bool {
	validation, ok := err.(*ValidationError)
	if ok {
		*target = validation
	}
	return ok
}

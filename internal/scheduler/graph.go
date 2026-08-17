// Package scheduler provides pipeline graph validation and analysis.
package scheduler

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	api "github.com/CYPT71/platform-factory/internal/core"
)

// Engine capability names a pipeline may require. Static capabilities
// are properties of the engine build; host-probed capabilities also
// exist in the registry (so validation accepts them) but their
// availability is only decided by the runner on the executing host,
// which fails closed when a required one is missing.
const (
	CapabilityArtifacts      = "artifacts"
	CapabilityCache          = "cache"
	CapabilityMemoryRlimit   = "memory-rlimit"
	CapabilityNetworkNone    = "network-none"
	CapabilityParallelStages = "parallel-stages"
	CapabilitySecrets        = "secrets"
	CapabilitySandbox        = "sandbox"
	CapabilityCgroupCPU      = "cgroup-cpu"
	CapabilityCgroupPIDs     = "cgroup-pids"
)

var knownCapabilities = map[string]bool{
	CapabilityArtifacts:      true,
	CapabilityCache:          true,
	CapabilityMemoryRlimit:   true,
	CapabilityNetworkNone:    true,
	CapabilityParallelStages: true,
	CapabilitySecrets:        true,
	CapabilitySandbox:        true,
	CapabilityCgroupCPU:      true,
	CapabilityCgroupPIDs:     true,
}

// KnownCapabilities returns every capability name this engine can
// negotiate, sorted.
func KnownCapabilities() []string {
	names := make([]string, 0, len(knownCapabilities))
	for name := range knownCapabilities {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validateRequiredCapabilities(names []string) []Issue {
	var issues []Issue
	seen := map[string]bool{}
	for index, name := range names {
		field := fmt.Sprintf("required_capabilities[%d]", index)
		if !knownCapabilities[name] {
			issues = append(issues, Issue{Path: field, Message: "unknown capability " + name})
		} else if seen[name] {
			issues = append(issues, Issue{Path: field, Message: "duplicates capability " + name})
		}
		seen[name] = true
	}
	return issues
}

// ValidateRequiredCapabilities is exported for backward compatibility.
func ValidateRequiredCapabilities(names []string) []Issue {
	return validateRequiredCapabilities(names)
}

// MaxStages is the maximum number of stages allowed in a pipeline.
const MaxStages = 10_000

// IDPattern is the regex pattern for valid stage IDs.
var IDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// MaxStages alias for backward compatibility.
const maxStages = MaxStages

// IDPattern alias for backward compatibility.
var idPattern = IDPattern

// Issue describes one deterministic validation failure.
type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationError contains all failures that can be reported safely in one
// pass. Issues are sorted by path and message.
type ValidationError struct {
	Issues []Issue `json:"issues"`
}

func (e *ValidationError) Error() string {
	if len(e.Issues) == 0 {
		return "invalid pipeline"
	}
	return fmt.Sprintf("invalid pipeline: %s: %s", e.Issues[0].Path, e.Issues[0].Message)
}

// Graph is the validated deterministic execution view of a pipeline.
type Graph struct {
	Order  []string   `json:"order"`
	Levels [][]string `json:"levels"`
}

// Analyze validates a pipeline and returns stable topological levels. Stages
// within a level are independent and may be executed concurrently.
func Analyze(definition api.Pipeline) (Graph, error) {
	issues, stages := validate(definition)
	if len(issues) > 0 {
		return Graph{}, newValidationError(issues)
	}
	graph, remaining := topological(stages)
	if len(remaining) > 0 {
		issues = append(issues, Issue{
			Path:    "stages",
			Message: "dependency cycle contains " + strings.Join(remaining, ", "),
		})
		return Graph{}, newValidationError(issues)
	}
	return graph, nil
}

// acceptedAPIVersions accepts both the current Platform Factory wire
// identifiers and the pre-rebrand secure-oci.dev ones, for the
// documented compatibility overlap window (docs/api-compatibility.md) -
// not a permanent dual-accept, but removing the legacy entries here is a
// deprecation with its own release-note requirement, not this rename.
var acceptedAPIVersions = []string{
	api.PipelineAlphaAPIVersion, api.PipelineAlphaLegacyAPIVersion,
	api.PipelineBetaAPIVersion, api.PipelineBetaLegacyAPIVersion,
	api.PipelineAPIVersion, api.PipelineLegacyAPIVersion,
}

func validate(definition api.Pipeline) ([]Issue, map[string]api.Stage) {
	var issues []Issue
	accepted := false
	for _, version := range acceptedAPIVersions {
		if definition.APIVersion == version {
			accepted = true
			break
		}
	}
	if !accepted {
		issues = append(issues, Issue{Path: "api_version", Message: "must be one of " + strings.Join(acceptedAPIVersions, ", ")})
	}
	if !idPattern.MatchString(definition.Name) {
		issues = append(issues, Issue{Path: "name", Message: "must be a lowercase DNS label"})
	}
	if len(definition.Stages) == 0 {
		issues = append(issues, Issue{Path: "stages", Message: "must contain at least one stage"})
	}
	if len(definition.Stages) > maxStages {
		issues = append(issues, Issue{Path: "stages", Message: "exceeds the 10000 stage limit"})
		return issues, map[string]api.Stage{}
	}

	inputs := make(map[string]bool, len(definition.Inputs))
	for _, input := range definition.Inputs {
		inputs[input.ID] = true
	}
	stages := make(map[string]api.Stage, len(definition.Stages))
	issues = append(issues, validateRequiredCapabilities(definition.RequiredCapabilities)...)
	issues = append(issues, validateInputs(definition.Inputs)...)
	for index, stage := range definition.Stages {
		prefix := fmt.Sprintf("stages[%d]", index)
		if !idPattern.MatchString(stage.ID) {
			issues = append(issues, Issue{Path: prefix + ".id", Message: "must be a lowercase DNS label"})
		} else if _, exists := stages[stage.ID]; exists {
			issues = append(issues, Issue{Path: prefix + ".id", Message: "duplicates stage " + stage.ID})
		} else {
			stages[stage.ID] = stage
		}
		issues = append(issues, validateStage(prefix, stage)...)
	}
	for index, stage := range definition.Stages {
		seen := map[string]bool{}
		for dependencyIndex, dependency := range stage.DependsOn {
			field := fmt.Sprintf("stages[%d].depends_on[%d]", index, dependencyIndex)
			if dependency == stage.ID {
				issues = append(issues, Issue{Path: field, Message: "must not reference its own stage"})
			} else if _, exists := stages[dependency]; !exists {
				issues = append(issues, Issue{Path: field, Message: "references unknown stage " + dependency})
			} else if seen[dependency] {
				issues = append(issues, Issue{Path: field, Message: "duplicates dependency " + dependency})
			}
			seen[dependency] = true
		}
		for mountIndex, mount := range stage.Mounts {
			if !inputs[mount.Source] {
				issues = append(issues, Issue{
					Path:    fmt.Sprintf("stages[%d].mounts[%d].source", index, mountIndex),
					Message: "references unknown input " + mount.Source,
				})
			}
		}
		for inputIndex, input := range stage.Inputs {
			field := fmt.Sprintf("stages[%d].inputs[%d]", index, inputIndex)
			producer, exists := stages[input.Stage]
			if !exists {
				issues = append(issues, Issue{Path: field + ".stage", Message: "references unknown stage " + input.Stage})
				continue
			}
			found := false
			for _, artifact := range producer.Outputs {
				found = found || artifact.Name == input.Name
			}
			if !found {
				issues = append(issues, Issue{Path: field + ".name", Message: "references unknown artifact " + input.Name})
			}
			if !seen[input.Stage] {
				issues = append(issues, Issue{Path: field + ".stage", Message: "producer must be declared in depends_on"})
			}
			if input.Target != "" && !cleanAbsolutePath(input.Target) {
				issues = append(issues, Issue{Path: field + ".target", Message: "must be a clean absolute path"})
			}
		}
	}
	issues = append(issues, validateOutputs(definition.Outputs, stages)...)
	return issues, stages
}

func validateInputs(inputs []api.Input) []Issue {
	var issues []Issue
	seen := map[string]bool{}
	for index, input := range inputs {
		prefix := fmt.Sprintf("inputs[%d]", index)
		if !idPattern.MatchString(input.ID) {
			issues = append(issues, Issue{Path: prefix + ".id", Message: "must be a lowercase DNS label"})
		} else if seen[input.ID] {
			issues = append(issues, Issue{Path: prefix + ".id", Message: "duplicates input " + input.ID})
		}
		seen[input.ID] = true
		if input.Kind == "" || input.Source == "" || strings.ContainsRune(input.Source, 0) {
			issues = append(issues, Issue{Path: prefix, Message: "requires kind and a non-empty, NUL-free source"})
		}
		if !validDigest(input.Digest) {
			issues = append(issues, Issue{Path: prefix + ".digest", Message: "must be a sha256 digest"})
		}
	}
	return issues
}

func validateStage(prefix string, stage api.Stage) []Issue {
	var issues []Issue
	if stage.Command.Executable == "" || strings.ContainsRune(stage.Command.Executable, 0) {
		issues = append(issues, Issue{Path: prefix + ".command.executable", Message: "must be non-empty and NUL-free"})
	}
	if stage.Command.WorkingDir != "" && !cleanAbsolutePath(stage.Command.WorkingDir) {
		issues = append(issues, Issue{Path: prefix + ".command.working_dir", Message: "must be a clean absolute path"})
	}
	for index, argument := range stage.Command.Args {
		if strings.ContainsRune(argument, 0) {
			issues = append(issues, Issue{Path: fmt.Sprintf("%s.command.args[%d]", prefix, index), Message: "must be NUL-free"})
		}
	}
	for key, value := range stage.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			issues = append(issues, Issue{Path: prefix + ".env", Message: "contains an invalid variable name"})
		}
		if strings.ContainsRune(value, 0) {
			issues = append(issues, Issue{Path: prefix + ".env." + key, Message: "must be NUL-free"})
		}
		if key == "PLATFORM_FACTORY_ROOT" {
			issues = append(issues, Issue{Path: prefix + ".env." + key, Message: "is reserved by the executor"})
		}
	}
	if stage.Network == "" {
		stage.Network = api.NetworkNone
	}
	if stage.Network != api.NetworkNone && stage.Network != api.NetworkResolve && stage.Network != api.NetworkFull {
		issues = append(issues, Issue{Path: prefix + ".network", Message: "must be none, resolve, or full"})
	}
	if stage.Network != "" && stage.Network != api.NetworkNone && len(stage.DependsOn) > 0 {
		issues = append(issues, Issue{
			Path:    prefix + ".network",
			Message: "network-enabled resolution stages must be DAG roots; every later stage is network none",
		})
	}
	if stage.Resources.CPUMilli < 0 || stage.Resources.MemoryMiB < 0 || stage.Resources.PIDs < 0 {
		issues = append(issues, Issue{Path: prefix + ".resources", Message: "limits must not be negative"})
	}
	if stage.Base != nil {
		if stage.Base.Reference == "" || !validDigest(stage.Base.Digest) {
			issues = append(issues, Issue{Path: prefix + ".base", Message: "requires a reference and pinned sha256 digest"})
		}
		if stage.Base.Platform != "" && stage.Base.Platform != "linux/amd64" && stage.Base.Platform != "linux/arm64" {
			issues = append(issues, Issue{Path: prefix + ".base.platform", Message: "must be linux/amd64 or linux/arm64"})
		}
	}
	issues = append(issues, validatePaths(prefix+".mounts", mountPaths(stage.Mounts))...)
	issues = append(issues, validatePaths(prefix+".secrets", secretPaths(stage.Secrets))...)
	issues = append(issues, validatePaths(prefix+".caches", cachePaths(stage.Caches))...)
	seenArtifacts := map[string]bool{}
	for index, output := range stage.Outputs {
		field := fmt.Sprintf("%s.outputs[%d]", prefix, index)
		if !idPattern.MatchString(output.Name) {
			issues = append(issues, Issue{Path: field + ".name", Message: "must be a lowercase DNS label"})
		}
		if seenArtifacts[output.Name] {
			issues = append(issues, Issue{Path: field + ".name", Message: "duplicates artifact " + output.Name})
		}
		seenArtifacts[output.Name] = true
		if !cleanAbsolutePath(output.Path) {
			issues = append(issues, Issue{Path: field + ".path", Message: "must be a clean absolute path"})
		}
	}
	return issues
}

func validateOutputs(outputs []api.Output, stages map[string]api.Stage) []Issue {
	var issues []Issue
	seen := map[string]bool{}
	for index, output := range outputs {
		prefix := fmt.Sprintf("outputs[%d]", index)
		if !idPattern.MatchString(output.Name) {
			issues = append(issues, Issue{Path: prefix + ".name", Message: "must be a lowercase DNS label"})
		} else if seen[output.Name] {
			issues = append(issues, Issue{Path: prefix + ".name", Message: "duplicates output " + output.Name})
		}
		seen[output.Name] = true
		stage, exists := stages[output.Stage]
		if !exists {
			issues = append(issues, Issue{Path: prefix + ".stage", Message: "references unknown stage " + output.Stage})
			continue
		}
		found := false
		for _, artifact := range stage.Outputs {
			found = found || artifact.Name == output.Artifact
		}
		if !found {
			issues = append(issues, Issue{Path: prefix + ".artifact", Message: "references unknown artifact " + output.Artifact})
		}
	}
	return issues
}

type namedPath struct {
	id     string
	target string
}

func mountPaths(values []api.Mount) []namedPath {
	result := make([]namedPath, 0, len(values))
	for _, value := range values {
		result = append(result, namedPath{id: value.Source, target: value.Target})
	}
	return result
}

func secretPaths(values []api.SecretReference) []namedPath {
	result := make([]namedPath, 0, len(values))
	for _, value := range values {
		result = append(result, namedPath{id: value.ID, target: value.Target})
	}
	return result
}

func cachePaths(values []api.CacheMount) []namedPath {
	result := make([]namedPath, 0, len(values))
	for _, value := range values {
		result = append(result, namedPath{id: value.ID, target: value.Target})
	}
	return result
}

func validatePaths(prefix string, values []namedPath) []Issue {
	var issues []Issue
	seen := map[string]bool{}
	for index, value := range values {
		field := fmt.Sprintf("%s[%d]", prefix, index)
		if value.id == "" || strings.ContainsRune(value.id, 0) {
			issues = append(issues, Issue{Path: field, Message: "requires a non-empty, NUL-free identifier"})
		}
		if !cleanAbsolutePath(value.target) {
			issues = append(issues, Issue{Path: field + ".target", Message: "must be a clean absolute path"})
		} else if seen[value.target] {
			issues = append(issues, Issue{Path: field + ".target", Message: "duplicates target " + value.target})
		}
		seen[value.target] = true
	}
	return issues
}

func cleanAbsolutePath(value string) bool {
	return value != "" && strings.HasPrefix(value, "/") && value != "/" && path.Clean(value) == value
}

// ValidDigest checks if a string is a valid sha256 digest.
func ValidDigest(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	for _, character := range strings.TrimPrefix(value, prefix) {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

// validDigest alias for backward compatibility.
var validDigest = ValidDigest

func newValidationError(issues []Issue) *ValidationError {
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Path == issues[j].Path {
			return issues[i].Message < issues[j].Message
		}
		return issues[i].Path < issues[j].Path
	})
	return &ValidationError{Issues: issues}
}

// NewValidationError is exported for backward compatibility.
var NewValidationError = newValidationError

func topological(stages map[string]api.Stage) (Graph, []string) {
	indegree := make(map[string]int, len(stages))
	dependents := make(map[string][]string, len(stages))
	for id, stage := range stages {
		indegree[id] = len(stage.DependsOn)
		for _, dependency := range stage.DependsOn {
			dependents[dependency] = append(dependents[dependency], id)
		}
	}
	var ready []string
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	graph := Graph{}
	for len(ready) > 0 {
		level := append([]string(nil), ready...)
		graph.Levels = append(graph.Levels, level)
		graph.Order = append(graph.Order, level...)
		var next []string
		for _, id := range level {
			sort.Strings(dependents[id])
			for _, dependent := range dependents[id] {
				indegree[dependent]--
				if indegree[dependent] == 0 {
					next = append(next, dependent)
				}
			}
		}
		sort.Strings(next)
		ready = next
	}
	var remaining []string
	for id, degree := range indegree {
		if degree > 0 {
			remaining = append(remaining, id)
		}
	}
	sort.Strings(remaining)
	return graph, remaining
}

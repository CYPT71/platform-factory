package v1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	v1alpha1 "github.com/CYPT71/platform-factory/api/pipeline/v1alpha1"
	v1beta1 "github.com/CYPT71/platform-factory/api/pipeline/v1beta1"
)

const maxDefinitionBytes = 1 << 20

type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}
type ValidationError struct {
	Issues []Issue `json:"issues"`
}

func (e *ValidationError) Error() string {
	if len(e.Issues) == 0 {
		return "invalid pipeline"
	}
	return fmt.Sprintf("invalid pipeline: %s: %s", e.Issues[0].Path, e.Issues[0].Message)
}

type Graph struct {
	Order  []string   `json:"order"`
	Levels [][]string `json:"levels"`
}

func Decode(input io.Reader) (Pipeline, Graph, error) {
	data, err := io.ReadAll(io.LimitReader(input, maxDefinitionBytes+1))
	if err != nil {
		return Pipeline{}, Graph{}, fmt.Errorf("read pipeline: %w", err)
	}
	if len(data) > maxDefinitionBytes {
		return Pipeline{}, Graph{}, errors.New("pipeline exceeds the 1 MiB definition limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var definition Pipeline
	if err := decoder.Decode(&definition); err != nil {
		return Pipeline{}, Graph{}, fmt.Errorf("decode pipeline: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Pipeline{}, Graph{}, errors.New("pipeline must contain exactly one JSON object")
	}
	graph, err := Analyze(definition)
	return definition, graph, err
}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var acceptedVersions = map[string]bool{APIVersion: true, LegacyAPIVersion: true, v1beta1.APIVersion: true, v1beta1.LegacyAPIVersion: true, v1alpha1.APIVersion: true, v1alpha1.LegacyAPIVersion: true}

func Analyze(definition Pipeline) (Graph, error) {
	var issues []Issue
	if !acceptedVersions[definition.APIVersion] {
		issues = append(issues, Issue{"api_version", "unsupported API version"})
	}
	if !idPattern.MatchString(definition.Name) {
		issues = append(issues, Issue{"name", "must be a lowercase DNS label"})
	}
	if len(definition.Stages) == 0 {
		issues = append(issues, Issue{"stages", "must contain at least one stage"})
	}
	if len(definition.Stages) > 10000 {
		issues = append(issues, Issue{"stages", "exceeds the 10000 stage limit"})
	}
	stages := make(map[string]Stage, len(definition.Stages))
	for i, s := range definition.Stages {
		p := fmt.Sprintf("stages[%d]", i)
		if !idPattern.MatchString(s.ID) {
			issues = append(issues, Issue{p + ".id", "must be a lowercase DNS label"})
		} else if _, ok := stages[s.ID]; ok {
			issues = append(issues, Issue{p + ".id", "duplicates stage " + s.ID})
		} else {
			stages[s.ID] = s
		}
		if s.Command.Executable == "" {
			issues = append(issues, Issue{p + ".command.executable", "must not be empty"})
		}
	}
	for i, s := range definition.Stages {
		seen := map[string]bool{}
		for j, d := range s.DependsOn {
			p := fmt.Sprintf("stages[%d].depends_on[%d]", i, j)
			if d == s.ID {
				issues = append(issues, Issue{p, "must not reference its own stage"})
			} else if _, ok := stages[d]; !ok {
				issues = append(issues, Issue{p, "references unknown stage " + d})
			} else if seen[d] {
				issues = append(issues, Issue{p, "duplicates dependency " + d})
			}
			seen[d] = true
		}
	}
	if len(issues) > 0 {
		sort.Slice(issues, func(i, j int) bool {
			if issues[i].Path == issues[j].Path {
				return issues[i].Message < issues[j].Message
			}
			return issues[i].Path < issues[j].Path
		})
		return Graph{}, &ValidationError{issues}
	}
	indegree := map[string]int{}
	dependents := map[string][]string{}
	for id, s := range stages {
		indegree[id] = len(s.DependsOn)
		for _, d := range s.DependsOn {
			dependents[d] = append(dependents[d], id)
		}
	}
	var order []string
	var levels [][]string
	ready := []string{}
	for id, n := range indegree {
		if n == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	for len(ready) > 0 {
		level := append([]string(nil), ready...)
		levels = append(levels, level)
		order = append(order, level...)
		next := []string{}
		for _, id := range level {
			sort.Strings(dependents[id])
			for _, d := range dependents[id] {
				indegree[d]--
				if indegree[d] == 0 {
					next = append(next, d)
				}
			}
		}
		sort.Strings(next)
		ready = next
	}
	if len(order) != len(stages) {
		var remaining []string
		for id, n := range indegree {
			if n > 0 {
				remaining = append(remaining, id)
			}
		}
		sort.Strings(remaining)
		return Graph{}, &ValidationError{[]Issue{{"stages", "dependency cycle contains " + strings.Join(remaining, ", ")}}}
	}
	return Graph{order, levels}, nil
}

func CanonicalJSON(definition Pipeline) ([]byte, error) {
	if _, err := Analyze(definition); err != nil {
		return nil, err
	}
	n := definition
	n.RequiredCapabilities = sorted(n.RequiredCapabilities)
	n.Inputs = append([]Input(nil), n.Inputs...)
	sort.Slice(n.Inputs, func(i, j int) bool { return n.Inputs[i].ID < n.Inputs[j].ID })
	n.Outputs = append([]Output(nil), n.Outputs...)
	sort.Slice(n.Outputs, func(i, j int) bool { return n.Outputs[i].Name < n.Outputs[j].Name })
	n.Stages = append([]Stage(nil), n.Stages...)
	for i := range n.Stages {
		n.Stages[i].DependsOn = sorted(n.Stages[i].DependsOn)
		if n.Stages[i].Network == "" {
			n.Stages[i].Network = NetworkNone
		}
		n.Stages[i].Command.Args = append([]string(nil), n.Stages[i].Command.Args...)
	}
	sort.Slice(n.Stages, func(i, j int) bool { return n.Stages[i].ID < n.Stages[j].ID })
	return json.Marshal(n)
}
func Fingerprint(definition Pipeline) (string, error) {
	data, err := CanonicalJSON(definition)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
func sorted(v []string) []string {
	r := append([]string(nil), v...)
	sort.Strings(r)
	if len(r) == 0 {
		return nil
	}
	return r
}

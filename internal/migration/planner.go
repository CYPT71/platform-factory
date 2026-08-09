package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/CYPT71/secure-oci-base/internal/core"
)

const planDigestDomain = "platform-factory/migration-plan/v1\x00"

type Step struct {
	OperationID core.OperationID
	ResourceID  string
	Capability  string
	Version     string
	Action      string
	DependsOn   []core.OperationID
}

type Plan struct {
	InputDigest string
	Discovery   DiscoveryStatus
	Resources   []Resource
	Edges       []DependencyEdge
	Steps       []Step
	Gaps        []CompatibilityGap
	Unknowns    []UnknownObservation
	Digest      string
}

// BuildPlan is pure: it validates and copies input before deriving a plan.
func BuildPlan(input Aggregate) (Plan, error) {
	if err := input.Validate(); err != nil {
		return Plan{}, err
	}
	if input.Discovery == DiscoveryFailed {
		return Plan{}, invalid("cannot plan from failed discovery")
	}
	canonical := input.Canonical()
	inputDigest, err := digestValue("platform-factory/migration-input/v1\x00", canonical)
	if err != nil {
		return Plan{}, err
	}
	steps, err := buildSteps(inputDigest, canonical.Resources, canonical.Edges)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{
		InputDigest: inputDigest,
		Discovery:   canonical.Discovery,
		Resources:   canonical.Resources,
		Edges:       canonical.Edges,
		Steps:       steps,
		Gaps:        canonical.Gaps,
		Unknowns:    canonical.Unknowns,
	}
	plan.Digest, err = plan.ComputeDigest()
	if err != nil {
		return Plan{}, err
	}
	if err := plan.Validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func buildSteps(inputDigest string, canonicalResources []Resource, canonicalEdges []DependencyEdge) ([]Step, error) {
	requiredEdges := make([]DependencyEdge, 0, len(canonicalEdges))
	for _, edge := range canonicalEdges {
		if edge.Required {
			requiredEdges = append(requiredEdges, edge)
		}
	}
	order, err := topologicalResourceIDs(canonicalResources, requiredEdges)
	if err != nil {
		return nil, err
	}
	resources := make(map[string]Resource, len(canonicalResources))
	dependencies := make(map[string]map[string]struct{}, len(canonicalResources))
	for _, resource := range canonicalResources {
		resources[resource.ID] = resource
	}
	for _, edge := range canonicalEdges {
		// Optional edges are preserved as graph evidence, but only required
		// edges constrain execution.
		if edge.Required {
			if dependencies[edge.From] == nil {
				dependencies[edge.From] = make(map[string]struct{})
			}
			dependencies[edge.From][edge.To] = struct{}{}
		}
	}

	steps := make([]Step, 0)
	operations := make(map[string][]core.OperationID, len(canonicalResources))
	for _, resourceID := range order {
		resource := resources[resourceID]
		for _, requirement := range resource.Requirements {
			action := "apply"
			operationID := operationID(inputDigest, resource.ID, requirement.Capability, requirement.Version, action)
			stepDependencies := make([]core.OperationID, 0)
			for dependencyID := range dependencies[resourceID] {
				stepDependencies = append(stepDependencies, operations[dependencyID]...)
			}
			sort.Slice(stepDependencies, func(i, j int) bool { return stepDependencies[i] < stepDependencies[j] })
			step := Step{OperationID: operationID, ResourceID: resource.ID, Capability: requirement.Capability, Version: requirement.Version, Action: action, DependsOn: stepDependencies}
			steps = append(steps, step)
			operations[resourceID] = append(operations[resourceID], operationID)
		}
	}
	return steps, nil
}

func (p Plan) ComputeDigest() (string, error) {
	p.Digest = ""
	return digestValue(planDigestDomain, p)
}

func (p Plan) VerifyDigest() error {
	if p.Digest == "" {
		return invalid("plan digest is required")
	}
	want, err := p.ComputeDigest()
	if err != nil {
		return err
	}
	if p.Digest != want {
		return invalid("plan digest does not match content")
	}
	return nil
}

func (p Plan) Validate() error {
	if invalidText(p.InputDigest) {
		return invalid("plan input digest is required")
	}
	aggregate := Aggregate{Discovery: p.Discovery, Resources: p.Resources, Edges: p.Edges, Gaps: p.Gaps, Unknowns: p.Unknowns}
	if err := aggregate.Validate(); err != nil {
		return fmt.Errorf("invalid plan aggregate: %w", err)
	}
	if aggregate.Discovery == DiscoveryFailed {
		return invalid("plan cannot represent failed discovery")
	}
	canonical := aggregate.Canonical()
	if !reflect.DeepEqual(aggregate, canonical) {
		return invalid("plan aggregate is not canonical")
	}
	wantInputDigest, err := digestValue("platform-factory/migration-input/v1\x00", canonical)
	if err != nil {
		return err
	}
	if p.InputDigest != wantInputDigest {
		return invalid("plan input digest does not match canonical aggregate")
	}
	wantSteps, err := buildSteps(p.InputDigest, canonical.Resources, canonical.Edges)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(p.Steps, wantSteps) {
		return invalid("plan steps do not match canonical resources, requirements, and required dependencies")
	}
	return p.VerifyDigest()
}

func operationID(inputDigest, resourceID, capability, version, action string) core.OperationID {
	sum := sha256.Sum256([]byte("platform-factory/migration-operation/v1\x00" + inputDigest + "\x00" + resourceID + "\x00" + capability + "\x00" + version + "\x00" + action))
	return core.OperationID("migration-" + hex.EncodeToString(sum[:]))
}

func digestValue(domain string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal canonical migration value: %w", err)
	}
	sum := sha256.Sum256(append([]byte(domain), data...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func topologicalResourceIDs(resources []Resource, edges []DependencyEdge) ([]string, error) {
	indegree := make(map[string]int, len(resources))
	children := make(map[string][]string, len(resources))
	for _, resource := range resources {
		indegree[resource.ID] = 0
	}
	for _, edge := range edges {
		// From depends on To: To must precede From.
		children[edge.To] = append(children[edge.To], edge.From)
		indegree[edge.From]++
	}
	ready := make([]string, 0, len(resources))
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	order := make([]string, 0, len(resources))
	for len(ready) != 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		sort.Strings(children[id])
		for _, child := range children[id] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
				sort.Strings(ready)
			}
		}
	}
	if len(order) != len(resources) {
		return nil, invalid("dependency graph contains a cycle")
	}
	return order, nil
}

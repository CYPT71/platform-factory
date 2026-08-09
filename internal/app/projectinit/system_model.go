package projectinit

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

type RuntimeMode string

const (
	RuntimeContainer RuntimeMode = "container"
	RuntimeMicroVM   RuntimeMode = "microvm"
)

type ResourceOwnership string

const (
	OwnershipExternal ResourceOwnership = "external"
	OwnershipShared   ResourceOwnership = "shared"
)

type DeletionPolicy string

const DeletionRetain DeletionPolicy = "retain"

// RuntimeDecision records both the recommendation and the accepted choice.
// An empty Selected value is valid only when the uncertainty is explicit.
type RuntimeDecision struct {
	Recommended RuntimeMode
	Selected    RuntimeMode
	Reasons     []string
	Unknowns    []Unknown
}

type ComponentProposal struct {
	Name    string
	Source  string
	Runtime RuntimeDecision
}

// ResourceProposal represents infrastructure that Platform Factory may use
// but does not own exclusively. Such resources can never be proposed for
// deletion by project initialization.
type ResourceProposal struct {
	Name           string
	Type           string
	Ownership      ResourceOwnership
	DeletionPolicy DeletionPolicy
}

type ConnectionProposal struct {
	From     string
	To       string
	Protocol string
	Port     uint16
}

// SystemProposal is the deterministic, reviewable result of system
// inspection. It is a proposal only: it does not build or deploy anything.
type SystemProposal struct {
	Name        string
	Components  []ComponentProposal
	Resources   []ResourceProposal
	Connections []ConnectionProposal
	Unknowns    []Unknown
}

// WithSystemProposal validates and canonicalizes proposal before attaching it
// to a copy of plan. The input and original plan are not mutated.
func WithSystemProposal(plan Plan, proposal SystemProposal) (Plan, error) {
	canonical := cloneSystemProposal(proposal)
	canonicalizeSystemProposal(&canonical)
	if err := canonical.Validate(); err != nil {
		return Plan{}, err
	}
	plan.Actions = cloneActions(plan.Actions)
	plan.Unknowns = slices.Clone(plan.Unknowns)
	plan.System = &canonical
	return plan, nil
}

func (p SystemProposal) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("project init proposal: system name is required")
	}
	if err := validateUnknowns("system", p.Unknowns); err != nil {
		return err
	}
	names := make(map[string]string, len(p.Components)+len(p.Resources))
	for _, component := range p.Components {
		if err := validateProposalName("component", component.Name, names); err != nil {
			return err
		}
		if err := validateRelativeSource(component.Name, component.Source); err != nil {
			return err
		}
		if err := validateRuntime(component.Name, component.Runtime); err != nil {
			return err
		}
	}
	for _, resource := range p.Resources {
		if err := validateProposalName("resource", resource.Name, names); err != nil {
			return err
		}
		if strings.TrimSpace(resource.Type) == "" {
			return fmt.Errorf("project init proposal: resource %q type is required", resource.Name)
		}
		if resource.Ownership != OwnershipExternal && resource.Ownership != OwnershipShared {
			return fmt.Errorf("project init proposal: resource %q has invalid ownership %q", resource.Name, resource.Ownership)
		}
		if resource.DeletionPolicy != DeletionRetain {
			return fmt.Errorf("project init proposal: resource %q must use retain deletion policy", resource.Name)
		}
	}
	connections := make(map[ConnectionProposal]struct{}, len(p.Connections))
	for _, connection := range p.Connections {
		if _, duplicate := connections[connection]; duplicate {
			return fmt.Errorf("project init proposal: duplicate connection from %q to %q", connection.From, connection.To)
		}
		connections[connection] = struct{}{}
		if connection.From == connection.To {
			return fmt.Errorf("project init proposal: connection endpoint %q cannot target itself", connection.From)
		}
		for _, endpoint := range []string{connection.From, connection.To} {
			if _, ok := names[endpoint]; !ok {
				return fmt.Errorf("project init proposal: connection references unknown endpoint %q", endpoint)
			}
		}
		if strings.TrimSpace(connection.Protocol) == "" {
			return fmt.Errorf("project init proposal: connection %q to %q requires a protocol", connection.From, connection.To)
		}
	}
	return nil
}

func validateProposalName(kind, name string, names map[string]string) error {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("project init proposal: %s name is required", kind)
	}
	if previous, exists := names[name]; exists {
		return fmt.Errorf("project init proposal: duplicate endpoint name %q (%s and %s)", name, previous, kind)
	}
	names[name] = kind
	return nil
}

func validateRelativeSource(name, source string) error {
	if source == "" || path.IsAbs(source) || path.Clean(source) != source || source == ".." || strings.HasPrefix(source, "../") || strings.Contains(source, `\`) {
		return fmt.Errorf("project init proposal: component %q source must be a clean relative path", name)
	}
	return nil
}

func validateRuntime(name string, runtime RuntimeDecision) error {
	if !validRuntime(runtime.Recommended) {
		return fmt.Errorf("project init proposal: component %q has invalid recommended runtime %q", name, runtime.Recommended)
	}
	if runtime.Selected != "" && !validRuntime(runtime.Selected) {
		return fmt.Errorf("project init proposal: component %q has invalid selected runtime %q", name, runtime.Selected)
	}
	if len(runtime.Reasons) == 0 {
		return fmt.Errorf("project init proposal: component %q runtime recommendation requires reasons", name)
	}
	for _, reason := range runtime.Reasons {
		if strings.TrimSpace(reason) == "" {
			return fmt.Errorf("project init proposal: component %q has an empty runtime reason", name)
		}
	}
	if err := validateUnknowns("component "+name+" runtime", runtime.Unknowns); err != nil {
		return err
	}
	if runtime.Selected == "" && len(runtime.Unknowns) == 0 {
		return fmt.Errorf("project init proposal: component %q runtime selection is unresolved without an explicit unknown", name)
	}
	return nil
}

func validRuntime(mode RuntimeMode) bool { return mode == RuntimeContainer || mode == RuntimeMicroVM }

func validateUnknowns(owner string, unknowns []Unknown) error {
	for _, unknown := range unknowns {
		if strings.TrimSpace(unknown.Subject) == "" || strings.TrimSpace(unknown.Reason) == "" {
			return fmt.Errorf("project init proposal: %s contains an incomplete unknown", owner)
		}
	}
	return nil
}

func cloneSystemProposal(in SystemProposal) SystemProposal {
	out := in
	out.Components = slices.Clone(in.Components)
	for i := range out.Components {
		out.Components[i].Runtime.Reasons = slices.Clone(in.Components[i].Runtime.Reasons)
		out.Components[i].Runtime.Unknowns = slices.Clone(in.Components[i].Runtime.Unknowns)
	}
	out.Resources = slices.Clone(in.Resources)
	out.Connections = slices.Clone(in.Connections)
	out.Unknowns = slices.Clone(in.Unknowns)
	return out
}

func cloneActions(in []Action) []Action {
	out := slices.Clone(in)
	for i := range out {
		out[i].content = slices.Clone(in[i].content)
	}
	return out
}

func canonicalizeSystemProposal(proposal *SystemProposal) {
	for i := range proposal.Components {
		slices.Sort(proposal.Components[i].Runtime.Reasons)
		sortUnknowns(proposal.Components[i].Runtime.Unknowns)
	}
	slices.SortFunc(proposal.Components, func(a, b ComponentProposal) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(proposal.Resources, func(a, b ResourceProposal) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(proposal.Connections, func(a, b ConnectionProposal) int {
		ak := fmt.Sprintf("%s\x00%s\x00%s\x00%05d", a.From, a.To, a.Protocol, a.Port)
		bk := fmt.Sprintf("%s\x00%s\x00%s\x00%05d", b.From, b.To, b.Protocol, b.Port)
		return strings.Compare(ak, bk)
	})
	sortUnknowns(proposal.Unknowns)
}

func sortUnknowns(unknowns []Unknown) {
	slices.SortFunc(unknowns, func(a, b Unknown) int {
		if order := strings.Compare(a.Subject, b.Subject); order != 0 {
			return order
		}
		return strings.Compare(a.Reason, b.Reason)
	})
}

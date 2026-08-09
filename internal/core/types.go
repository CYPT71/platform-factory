package core

import "fmt"

type WorkloadID string
type OperationID string
type ArtifactDigest string
type PluginID string

type WorkloadSpec struct {
	ID      WorkloadID `json:"id,omitempty"`
	Version string     `json:"version,omitempty"`
}

func (s WorkloadSpec) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("workload id is required")
	}
	if s.Version == "" {
		return fmt.Errorf("workload version is required")
	}
	return nil
}

type BuildPlan struct {
	WorkloadID WorkloadID `json:"workload_id,omitempty"`
}

func (p BuildPlan) Validate() error {
	if p.WorkloadID == "" {
		return fmt.Errorf("workload id is required")
	}
	return nil
}

type ArtifactDescriptor struct {
	Digest ArtifactDigest `json:"digest,omitempty"`
}

func (a ArtifactDescriptor) Validate() error {
	if a.Digest == "" {
		return fmt.Errorf("artifact digest is required")
	}
	return nil
}

type RuntimeSpec struct {
	Kind string `json:"kind,omitempty"`
}

func (r RuntimeSpec) Validate() error {
	if r.Kind == "" {
		return fmt.Errorf("runtime kind is required")
	}
	return nil
}

type DeploymentSpec struct {
	Runtime RuntimeSpec `json:"runtime,omitempty"`
}

func (d DeploymentSpec) Validate() error {
	return d.Runtime.Validate()
}

// RuntimeState is a workload's canonical state - see statemachine.go for
// the Phase values it may hold and the transitions between them. This is
// the single state every backend (containerd, KubeVirt, Kubernetes,
// Docker, Podman) translates its own native state into; none of them
// invent their own meaning for what "running" means.
type RuntimeState struct {
	Phase Phase `json:"phase,omitempty"`
}

type EvidenceBundle struct {
	Artifacts []ArtifactDescriptor `json:"artifacts,omitempty"`
}

func (e EvidenceBundle) Validate() error {
	for _, artifact := range e.Artifacts {
		if err := artifact.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type CapabilitySet struct {
	Capabilities []string `json:"capabilities,omitempty"`
}

type PolicyDecision struct {
	Allowed bool `json:"allowed"`
}

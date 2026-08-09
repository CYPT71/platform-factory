package v1alpha1

const (
	APIVersion = "platform-factory.dev/v1alpha1"
	// LegacyAPIVersion is the pre-rebrand identifier this package
	// accepted before Platform Factory: still decodable during the
	// documented two-minor-release compatibility overlap window (see
	// docs/api-compatibility.md), never written by anything new.
	LegacyAPIVersion = "secure-oci.dev/v1alpha1"
)

// Pipeline is a declarative directed acyclic graph of build stages.
type Pipeline struct {
	APIVersion string `json:"api_version"`
	Name       string `json:"name"`
	// RequiredCapabilities names engine capabilities this pipeline
	// depends on. Unknown names are rejected at validation; an engine
	// that recognizes a name but cannot honor it on the current host
	// fails closed before executing any stage.
	RequiredCapabilities []string `json:"required_capabilities,omitempty"`
	Inputs               []Input  `json:"inputs,omitempty"`
	Stages               []Stage  `json:"stages"`
	Outputs              []Output `json:"outputs,omitempty"`
}

// Input identifies immutable source material consumed by the pipeline.
type Input struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Digest string `json:"digest,omitempty"`
}

// Stage is the language-neutral unit scheduled by the pipeline engine.
type Stage struct {
	ID        string                `json:"id"`
	DependsOn []string              `json:"depends_on,omitempty"`
	Base      *ImageReference       `json:"base,omitempty"`
	Command   Command               `json:"command"`
	Env       map[string]string     `json:"env,omitempty"`
	Mounts    []Mount               `json:"mounts,omitempty"`
	Secrets   []SecretReference     `json:"secrets,omitempty"`
	Caches    []CacheMount          `json:"caches,omitempty"`
	Inputs    []ArtifactReference   `json:"inputs,omitempty"`
	Outputs   []ArtifactDeclaration `json:"outputs,omitempty"`
	Network   NetworkPolicy         `json:"network,omitempty"`
	Resources ResourceLimits        `json:"resources,omitempty"`
	Sandbox   SandboxPolicy         `json:"sandbox,omitempty"`
}

type Command struct {
	Executable string   `json:"executable"`
	Args       []string `json:"args,omitempty"`
	WorkingDir string   `json:"working_dir,omitempty"`
}

type ImageReference struct {
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
	Platform  string `json:"platform,omitempty"`
}

type Mount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

type SecretReference struct {
	ID     string `json:"id"`
	Target string `json:"target"`
}

type CacheMount struct {
	ID     string `json:"id"`
	Target string `json:"target"`
}

type ArtifactReference struct {
	Stage string `json:"stage"`
	Name  string `json:"name"`
	// Target is the clean absolute path the referenced artifact is
	// materialized at inside the consuming stage. Empty defaults to
	// /inputs/<stage>/<name>. The content is copied from the
	// content-addressed store and its digest re-verified at
	// consumption time.
	Target string `json:"target,omitempty"`
}

type ArtifactDeclaration struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Output struct {
	Name     string `json:"name"`
	Stage    string `json:"stage"`
	Artifact string `json:"artifact"`
}

type NetworkPolicy string

const (
	NetworkNone    NetworkPolicy = "none"
	NetworkResolve NetworkPolicy = "resolve"
	NetworkFull    NetworkPolicy = "full"
)

type ResourceLimits struct {
	CPUMilli  int64 `json:"cpu_milli,omitempty"`
	MemoryMiB int64 `json:"memory_mib,omitempty"`
	PIDs      int64 `json:"pids,omitempty"`
}

type SandboxPolicy struct {
	ReadOnlyRoot bool `json:"read_only_root"`
	NonRoot      bool `json:"non_root"`
}

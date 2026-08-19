package product

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/CYPT71/platform-factory/internal/mcp/toolerror"
)

type deployArguments struct {
	Image         string   `json:"image"`
	Name          string   `json:"name"`
	Namespace     string   `json:"namespace"`
	Replicas      int      `json:"replicas"`
	Port          int      `json:"port"`
	Workload      string   `json:"workload"`
	Schedule      string   `json:"schedule"`
	CPURequest    string   `json:"cpu_request"`
	MemoryRequest string   `json:"memory_request"`
	RuntimeClass  string   `json:"runtime_class"`
	IngressHost   string   `json:"ingress_host"`
	IngressPath   string   `json:"ingress_path"`
	Config        []string `json:"config"`
	SecretEnv     []string `json:"secret_env"`
	Volumes       []string `json:"volumes"`
	Timeout       string   `json:"timeout"`
	Reports       string   `json:"reports"`
	Policy        string   `json:"policy"`
	Evidence      string   `json:"evidence"`
	DryRun        bool     `json:"dry_run"`
	Yes           bool     `json:"yes"`
	ExtraArgs     []string `json:"extra_args"`
	ProjectRoot   string   `json:"project_root"`
}

// DeployToolHandler returns the pf_deploy handler: `platform-factory
// deploy`, applying a digest-pinned image to Kubernetes. Secret values
// are never accepted as a tool argument, only ENV=SECRET/KEY references
// (--secret-env) the cluster itself resolves - matching --secret-env's
// own documented contract that values are never read by this command.
func DeployToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var a deployArguments
		if len(arguments) > 0 && string(arguments) != "{}" {
			if err := json.Unmarshal(arguments, &a); err != nil {
				return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
			}
		}
		if err := validExtraArgs(a.ExtraArgs); err != nil {
			return "", err
		}
		root, err := resolveProjectRoot(repoRoot, a.ProjectRoot)
		if err != nil {
			return "", err
		}
		reports, err := scopedRelative(root, a.Reports)
		if err != nil {
			return "", err
		}
		policy, err := scopedRelative(root, a.Policy)
		if err != nil {
			return "", err
		}
		evidence, err := scopedRelative(root, a.Evidence)
		if err != nil {
			return "", err
		}

		var args []string
		args = stringFlag(args, "--name", a.Name)
		args = stringFlag(args, "--namespace", a.Namespace)
		if a.Replicas > 0 {
			args = append(args, "--replicas", strconv.Itoa(a.Replicas))
		}
		if a.Port > 0 {
			args = append(args, "--port", strconv.Itoa(a.Port))
		}
		args = stringFlag(args, "--workload", a.Workload)
		args = stringFlag(args, "--schedule", a.Schedule)
		args = stringFlag(args, "--cpu-request", a.CPURequest)
		args = stringFlag(args, "--memory-request", a.MemoryRequest)
		args = stringFlag(args, "--runtime-class", a.RuntimeClass)
		args = stringFlag(args, "--ingress-host", a.IngressHost)
		args = stringFlag(args, "--ingress-path", a.IngressPath)
		for _, c := range a.Config {
			args = append(args, "--config", c)
		}
		for _, s := range a.SecretEnv {
			args = append(args, "--secret-env", s)
		}
		for _, v := range a.Volumes {
			args = append(args, "--volume", v)
		}
		args = stringFlag(args, "--timeout", a.Timeout)
		args = stringFlag(args, "--reports", reports)
		args = stringFlag(args, "--policy", policy)
		args = stringFlag(args, "--evidence", evidence)
		args = boolFlag(args, "--dry-run", a.DryRun)
		args = boolFlag(args, "--yes", a.Yes)
		args = append(args, a.ExtraArgs...)
		if a.Image != "" {
			args = append(args, a.Image)
		}

		result, err := run(ctx, root, "deploy", args)
		if err != nil {
			return "", err
		}
		return encode(result)
	}
}

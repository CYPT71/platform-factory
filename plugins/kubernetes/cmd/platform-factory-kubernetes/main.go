// Command platform-factory-kubernetes is the Kubernetes backend for
// platform-factory deploy/rollback/observe: it applies a generated
// manifest, waits for workloads to become ready, reads status, and rolls
// a Deployment back to a prior revision, all through a real Kubernetes
// API client (k8s.io/client-go) rather than shelling out to kubectl.
//
// It speaks the same out-of-process plugin wire protocol every other
// capability plugin does (sdk/plugin), discovered, verified and
// dispatched by capability (deployment.apply, deployment.observe,
// deployment.rollback) through internal/plugin.Registry/Client - see
// plugins/kubevirt/cmd/platform-factory-kubevirt for the closest
// existing precedent this follows: a real plugin subprocess that
// declares Permissions.Network and Permissions.Secrets:["kubeconfig"]
// in its own plugin.json so the host's sandbox (see
// internal/plugin/sandbox_linux.go's hostNetworkGranted/
// declaresKubeconfigSecret) grants it the real network access and
// KUBECONFIG/HOME visibility a cluster client genuinely needs, which the
// default plugin sandbox (an isolated, connectivity-less network
// namespace with a filtered environment) otherwise denies. Unlike that
// precedent, this plugin does not shell out to kubectl/virtctl at all -
// see plugins/kubernetes/kubernetes.go for the real API client calls.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	pfkubernetes "github.com/CYPT71/platform-factory/plugins/kubernetes"
	plugin "github.com/CYPT71/platform-factory/sdk/plugin"
)

// defaultTimeout backs every observe/rollback wait when the caller's own
// Timeout field is empty or malformed - the same "2m" default
// cmd/platform-factory/lifecycle.go's --timeout flag already used before
// this plugin existed.
const defaultTimeout = 2 * time.Minute

func parseTimeout(value string) time.Duration {
	if value == "" {
		return defaultTimeout
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return defaultTimeout
	}
	return parsed
}

// newClient is a var, not a direct call to
// pfkubernetes.NewClientFromKubeconfig, so tests can substitute a stub
// that never dials a real cluster.
var newClient = pfkubernetes.NewClientFromKubeconfig

func main() {
	server := plugin.NewServer("kubernetes", "1.0.0")
	server.Handle(plugin.CapabilityDeploymentApply, handleApply)
	server.Handle(plugin.CapabilityDeploymentObserve, handleObserve)
	server.Handle(plugin.CapabilityDeploymentRollback, handleRollback)
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "platform-factory-kubernetes:", err)
		os.Exit(1)
	}
}

func handleApply(ctx context.Context, raw json.RawMessage) (any, error) {
	var params plugin.DeploymentApplyParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("decode params: %w", err)
	}
	if len(params.Manifest) == 0 {
		return nil, errors.New("params.manifest is required")
	}
	client, err := newClient()
	if err != nil {
		return nil, err
	}
	resources, err := client.Apply(ctx, params.Manifest)
	if err != nil {
		return nil, err
	}
	return plugin.DeploymentApplyResult{Applied: true, Resources: resources}, nil
}

func handleObserve(ctx context.Context, raw json.RawMessage) (any, error) {
	var params plugin.DeploymentObserveParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("decode params: %w", err)
	}
	if params.Namespace == "" || params.Name == "" {
		return nil, errors.New("params.namespace and params.name are required")
	}
	client, err := newClient()
	if err != nil {
		return nil, err
	}
	switch params.Kind {
	case "wait-job":
		ready, output, err := client.WaitForJobComplete(ctx, params.Namespace, params.Name, parseTimeout(params.Timeout))
		if err != nil {
			return nil, err
		}
		return plugin.DeploymentObserveResult{Output: output, Ready: ready}, nil
	case "get-cronjob":
		output, err := client.GetCronJobSummary(ctx, params.Namespace, params.Name)
		if err != nil {
			return nil, err
		}
		return plugin.DeploymentObserveResult{Output: output, Ready: true}, nil
	case "rollout-status":
		if params.ResourceType == "" {
			return nil, errors.New("params.resource_type is required for rollout-status")
		}
		ready, output, err := client.RolloutStatus(ctx, params.ResourceType, params.Namespace, params.Name, parseTimeout(params.Timeout))
		if err != nil {
			return nil, err
		}
		return plugin.DeploymentObserveResult{Output: output, Ready: ready}, nil
	case "logs":
		output, err := client.Logs(ctx, params.Namespace, params.Name, params.Tail, params.Follow)
		if err != nil {
			return nil, err
		}
		return plugin.DeploymentObserveResult{Output: output, Ready: true}, nil
	case "events":
		output, err := client.Events(ctx, params.Namespace, params.Name)
		if err != nil {
			return nil, err
		}
		return plugin.DeploymentObserveResult{Output: output, Ready: true}, nil
	default:
		return nil, fmt.Errorf("unsupported observe kind %q", params.Kind)
	}
}

func handleRollback(ctx context.Context, raw json.RawMessage) (any, error) {
	var params plugin.DeploymentRollbackParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("decode params: %w", err)
	}
	if params.Namespace == "" || params.Name == "" {
		return nil, errors.New("params.namespace and params.name are required")
	}
	client, err := newClient()
	if err != nil {
		return nil, err
	}
	revision, err := client.RolloutUndo(ctx, params.Namespace, params.Name, params.ToRevision)
	if err != nil {
		return nil, err
	}
	return plugin.DeploymentRollbackResult{RevisionApplied: revision}, nil
}

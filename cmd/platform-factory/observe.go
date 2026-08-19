package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	observeapp "github.com/CYPT71/platform-factory/internal/app/observe"
	api "github.com/CYPT71/platform-factory/sdk/plugin"
)

// runProjectObservation backs `platform-factory logs`/`events`: both are
// read-only cluster observations, dispatched through the deployment
// plugin's observe capability (see plugins/kubernetes) the same way
// deploy/rollback now dispatch their own Kubernetes operations -
// lifecycle.go's deployToCluster/rollbackCluster - rather than shelling
// out to a kubectl binary that platform-factory-mcp's distroless
// container image does not ship. Read-only, so this uses
// pluginFlags.start (no operation journal) rather than startWithJournal,
// the same split detect/freeze/plan already use.
func runProjectObservation(ctx context.Context, command string, args []string, stdout, stderr io.Writer, execute containerExecutor) int {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	follow := flags.Bool("follow", false, "follow log output")
	tail := flags.Int("tail", 200, "maximum initial log lines")
	pluginFlags := registerPluginFlags(flags)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *tail < 1 || (command == "events" && *follow) {
		fmt.Fprintf(stderr, "usage: platform-factory %s [--tail N]", command)
		if command == "logs" {
			fmt.Fprint(stderr, " [--follow]")
		}
		fmt.Fprintln(stderr)
		return 2
	}
	state, err := observeapp.LoadDeployedProject()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory %s: no deployed project (run `pf deploy` first): %v\n", command, err)
		return 1
	}
	// Kubernetes operations now go through the deployment plugin (see
	// below), not a shelled-out kubectl; execute stays in the signature
	// only so callers/tests across the CLI command boundary don't churn.
	_ = execute
	host, err := pluginFlags.start(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory %s: %v\n", command, err)
		return 1
	}
	defer host.Close()
	output, err := dispatchObservation(ctx, host, command, state.Namespace, state.Name, *tail, *follow)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory %s: %v\n", command, err)
		return 1
	}
	fmt.Fprint(stdout, output)
	return 0
}

// dispatchObservation is runProjectObservation's testable core: given an
// already discovered-and-started pluginHost (real, or in tests a stub -
// the same separation dispatchKubeVirt/deployToCluster/rollbackCluster
// already use), it calls the deployment plugin's observe capability for
// command ("logs" or "events") and returns its rendered output.
func dispatchObservation(ctx context.Context, host *pluginHost, command, namespace, name string, tail int, follow bool) (string, error) {
	client, found := host.findCapability(api.CapabilityDeploymentObserve)
	if !found {
		return "", fmt.Errorf("no installed plugin provides %s; pass --plugin-dir pointing at a directory containing the kubernetes plugin (see docs/containerd-kubernetes.md)", api.CapabilityDeploymentObserve)
	}
	params := api.DeploymentObserveParams{Namespace: namespace, Name: name}
	if command == "logs" {
		params.Kind = "logs"
		params.Tail = tail
		params.Follow = follow
	} else {
		params.Kind = "events"
	}
	var result api.DeploymentObserveResult
	if err := client.Call(ctx, "v1."+api.CapabilityDeploymentObserve, params, &result); err != nil {
		return "", err
	}
	return result.Output, nil
}

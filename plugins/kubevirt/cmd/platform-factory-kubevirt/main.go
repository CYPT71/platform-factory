// Command platform-factory-kubevirt is the KubeVirt backend for platform-factory
// microvm: it renders VirtualMachine/RBAC manifests and drives kubectl/virtctl
// through a MicroVM's KubeVirt lifecycle.
//
// It speaks the same out-of-process plugin wire protocol every other
// capability plugin does (sdk/plugin), discovered, verified and dispatched
// by capability (runtime.create, runtime.start, ...) through
// internal/plugin.Registry/Client - not exec'd directly by a hardcoded
// binary name the way an earlier version of this package's host-side
// caller (cmd/platform-factory/microvm.go's runKubeVirt) used to. This is
// what lets --backend=kubevirt actually go through the
// declared->discovered->negotiated->verified->available plugin lifecycle,
// and what makes this plugin subject to the same sandbox, resource
// ceilings and permission-gated network/credential access as any other
// plugin (see internal/plugin/sandbox_linux.go's hostNetworkGranted and
// declaresKubeconfigSecret) - this plugin's own plugin.json declares both
// Permissions.Network and Permissions.Secrets:["kubeconfig"] because
// kubectl/virtctl genuinely need to reach a real cluster, which the
// default plugin sandbox (an isolated, connectivity-less network
// namespace) would otherwise make impossible.
//
// See plugins/containerd/cmd/platform-factory-shim for the equivalent
// boundary applied to containerd - that one cannot follow this same
// pattern because it is invoked by containerd itself as a containerd
// shim, under containerd's own process supervision and wire protocol, not
// by platform-factory.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/CYPT71/platform-factory/plugins/kubevirt"
	microvm "github.com/CYPT71/platform-factory/sdk/microvm"
	plugin "github.com/CYPT71/platform-factory/sdk/plugin"
)

// executor runs an external command with optional stdin and returns its
// combined stdout. Swappable in tests so no test needs a real kubectl or
// virtctl on PATH.
type executor func(name string, args []string, stdin []byte) ([]byte, error)

var execCommand executor = runCommand

func runCommand(name string, args []string, stdin []byte) ([]byte, error) {
	command := exec.Command(name, args...)
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = os.Stderr
	err := command.Run()
	return stdout.Bytes(), err
}

func main() {
	server := plugin.NewServer("kubevirt", "1.0.0")
	server.Handle(plugin.CapabilityRuntimeCreate, handleCreate)
	server.Handle(plugin.CapabilityRuntimeStart, handleLifecycle("start"))
	server.Handle(plugin.CapabilityRuntimeStop, handleLifecycle("stop"))
	server.Handle(plugin.CapabilityRuntimeRestart, handleLifecycle("restart"))
	server.Handle(plugin.CapabilityRuntimeStatus, handleStatus)
	server.Handle(plugin.CapabilityRuntimeLogs, handleLogs)
	server.Handle(plugin.CapabilityRuntimeDelete, handleDelete)
	server.Handle(plugin.CapabilityRuntimeRBAC, handleRBAC)
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "platform-factory-kubevirt:", err)
		os.Exit(1)
	}
}

// specParams is the wire shape every handler here decodes: the fields of
// microvm.Spec a caller can meaningfully set, explicitly named and
// JSON-tagged rather than reusing sdk/microvm.Spec's own untagged Go field
// names directly on the wire - this plugin's RPC contract stays stable
// even if Spec's internal field names ever change.
type specParams struct {
	Name          string   `json:"name"`
	Namespace     string   `json:"namespace"`
	Image         string   `json:"image,omitempty"`
	Arch          string   `json:"arch,omitempty"`
	MemoryMiB     int      `json:"memory_mib,omitempty"`
	VCPUs         int      `json:"vcpus,omitempty"`
	ListenAddress string   `json:"listen_address,omitempty"`
	Publishes     []string `json:"publishes,omitempty"`
	Apply         bool     `json:"apply,omitempty"`
}

// defaultMemoryMiB, defaultVCPUs, defaultArch and defaultListenAddress
// mirror the previous flag-based CLI's own defaults (--memory-mib 128,
// --vcpus 1, --arch runtime.GOARCH, --listen-address 127.0.0.1): a caller
// that omits these JSON fields (their Go zero value) gets the same guest
// sizing a caller who omitted the equivalent flag used to get, not a spec
// that fails kubevirt.Validate for having asked for 0 memory or no
// architecture. cmd/platform-factory/microvm.go's own --backend=kubevirt
// flags already default these the same way before ever reaching this
// plugin, so these defaults matter chiefly for any other caller driving
// this plugin's RPC surface directly.
const defaultMemoryMiB = 128
const defaultVCPUs = 1
const defaultListenAddress = "127.0.0.1"

func (p specParams) spec() (microvm.Spec, error) {
	memoryMiB, vcpus, arch, listen := p.MemoryMiB, p.VCPUs, p.Arch, p.ListenAddress
	if memoryMiB == 0 {
		memoryMiB = defaultMemoryMiB
	}
	if vcpus == 0 {
		vcpus = defaultVCPUs
	}
	if arch == "" {
		arch = runtime.GOARCH
	}
	if listen == "" {
		listen = defaultListenAddress
	}
	spec := microvm.Spec{
		Name: p.Name, Namespace: p.Namespace, Image: p.Image, Arch: arch,
		Listen: listen, MemoryMiB: memoryMiB, VCPUs: vcpus, Port: 8080,
	}
	for _, value := range p.Publishes {
		forward, err := microvm.ParseForward(value)
		if err != nil {
			return microvm.Spec{}, err
		}
		spec.Forwards = append(spec.Forwards, forward)
	}
	if len(spec.Forwards) == 0 {
		spec.Forwards = []microvm.Forward{{
			HostIP: spec.Listen, HostPort: spec.Port, GuestPort: spec.Port, Protocol: "tcp",
		}}
	} else {
		spec.Port = spec.Forwards[0].HostPort
	}
	return spec, nil
}

func decodeSpecParams(raw json.RawMessage) (specParams, error) {
	var params specParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return specParams{}, fmt.Errorf("decode params: %w", err)
	}
	if params.Name == "" || params.Namespace == "" {
		return specParams{}, errors.New("params.name and params.namespace are required")
	}
	return params, nil
}

// manifestResult is the result of a dry-run-by-default rendering action
// (create, rbac): Manifest is always populated, Applied is true only when
// the caller asked to apply it and the apply command succeeded.
type manifestResult struct {
	Manifest string `json:"manifest"`
	Applied  bool   `json:"applied"`
	Output   string `json:"output,omitempty"`
}

// commandResult carries the captured output of a lifecycle/observation
// command (start/stop/restart/status/logs/delete).
type commandResult struct {
	Output string `json:"output,omitempty"`
}

func handleCreate(_ context.Context, raw json.RawMessage) (any, error) {
	params, err := decodeSpecParams(raw)
	if err != nil {
		return nil, err
	}
	spec, err := params.spec()
	if err != nil {
		return nil, err
	}
	if err := kubevirt.Validate(spec); err != nil {
		return nil, err
	}
	manifest, err := kubevirt.VirtualMachine(spec)
	if err != nil {
		return nil, err
	}
	if !params.Apply {
		return manifestResult{Manifest: string(manifest)}, nil
	}
	output, err := execCommand("kubectl", []string{"apply", "-f", "-"}, manifest)
	if err != nil {
		return nil, fmt.Errorf("kubectl apply: %w", err)
	}
	return manifestResult{Manifest: string(manifest), Applied: true, Output: string(output)}, nil
}

func handleRBAC(_ context.Context, raw json.RawMessage) (any, error) {
	params, err := decodeSpecParams(raw)
	if err != nil {
		return nil, err
	}
	spec, err := params.spec()
	if err != nil {
		return nil, err
	}
	manifest, err := kubevirt.RBAC(spec)
	if err != nil {
		return nil, err
	}
	if !params.Apply {
		return manifestResult{Manifest: string(manifest)}, nil
	}
	output, err := execCommand("kubectl", []string{"apply", "-f", "-"}, manifest)
	if err != nil {
		return nil, fmt.Errorf("kubectl apply: %w", err)
	}
	return manifestResult{Manifest: string(manifest), Applied: true, Output: string(output)}, nil
}

// handleLifecycle returns a handler that validates the target and drives
// virtctl <action> for it - shared by start/stop/restart, which differ only
// in the verb virtctl itself takes.
func handleLifecycle(action string) plugin.Handler {
	return func(_ context.Context, raw json.RawMessage) (any, error) {
		params, err := decodeSpecParams(raw)
		if err != nil {
			return nil, err
		}
		spec, err := params.spec()
		if err != nil {
			return nil, err
		}
		if err := kubevirt.ValidateTarget(spec); err != nil {
			return nil, err
		}
		output, err := execCommand("virtctl", []string{action, "--namespace", spec.Namespace, spec.Name}, nil)
		if err != nil {
			return nil, fmt.Errorf("virtctl %s: %w", action, err)
		}
		return commandResult{Output: string(output)}, nil
	}
}

func handleStatus(_ context.Context, raw json.RawMessage) (any, error) {
	params, err := decodeSpecParams(raw)
	if err != nil {
		return nil, err
	}
	spec, err := params.spec()
	if err != nil {
		return nil, err
	}
	if err := kubevirt.ValidateTarget(spec); err != nil {
		return nil, err
	}
	output, err := execCommand("kubectl", []string{"get", "virtualmachine", "--namespace", spec.Namespace, spec.Name, "-o", "json"}, nil)
	if err != nil {
		return nil, fmt.Errorf("kubectl get: %w", err)
	}
	return commandResult{Output: string(output)}, nil
}

func handleLogs(_ context.Context, raw json.RawMessage) (any, error) {
	params, err := decodeSpecParams(raw)
	if err != nil {
		return nil, err
	}
	spec, err := params.spec()
	if err != nil {
		return nil, err
	}
	if err := kubevirt.ValidateTarget(spec); err != nil {
		return nil, err
	}
	output, err := execCommand("virtctl", []string{"console", "--namespace", spec.Namespace, spec.Name}, nil)
	if err != nil {
		return nil, fmt.Errorf("virtctl console: %w", err)
	}
	return commandResult{Output: string(output)}, nil
}

func handleDelete(_ context.Context, raw json.RawMessage) (any, error) {
	params, err := decodeSpecParams(raw)
	if err != nil {
		return nil, err
	}
	spec, err := params.spec()
	if err != nil {
		return nil, err
	}
	if err := kubevirt.ValidateTarget(spec); err != nil {
		return nil, err
	}
	output, err := execCommand("kubectl", []string{"delete", "virtualmachine", "--namespace", spec.Namespace, spec.Name}, nil)
	if err != nil {
		return nil, fmt.Errorf("kubectl delete: %w", err)
	}
	return commandResult{Output: string(output)}, nil
}

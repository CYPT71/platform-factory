//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/containerd/containerd/api/types"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/shim"
	"github.com/containerd/containerd/v2/version"
)

// runtimeName is the containerd runtime_type this shim answers to.
// shim.BinaryName derives the executable containerd looks for on PATH from
// its last two dot-separated segments - "platform-factory" and "v1" here -
// giving containerd-shim-platform-factory-v1, which is what the built binary
// must be installed as (see plugins/containerd/internal/containerdshim).
const runtimeName = "io.containerd.platform-factory.v1"

// shimManager launches and tracks the OS process for one shim instance per
// containerd shim.group (normally one per pod: containers in the same pod
// share their sandbox's shim process, matching io.kubernetes.cri.sandbox-id).
// It intentionally has no cgroup or process-tree bookkeeping of its own:
// each container this shim's Task service creates is its own independently
// isolated MicroVM, supervised (and cleaned up on failure) entirely by
// internal/ociruntime via the existing platform-factory-runtime binary - the same
// path already proven under Podman. This type only answers containerd's
// "start a new shim process" / "stop that process" / "describe yourself"
// questions; see task_service.go and sandbox_service.go for the actual
// TTRPC surface the started process serves.
type shimManager struct{}

func (shimManager) Name() string { return runtimeName }

// groupLabel is the only OCI spec annotation this shim consults to decide
// whether a new container should join an already-running shim process
// rather than starting a new one. containerd's CRI plugin sets it to the
// pod sandbox ID for every container in that pod.
const groupLabel = "io.kubernetes.cri.sandbox-id"

// allowedContainerdSocketEnv pins the containerd socket accepted by the shim.
const allowedContainerdSocketEnv = "PLATFORM_FACTORY_SHIM_ALLOWED_CONTAINERD_SOCKET"

// allowedContainerdSocket validates address (opts.Address, containerd's own
// daemon socket path for this shim to dial/report back to) before Start
// uses it for anything. Structural validation (non-empty, no NUL byte, an
// absolute path) always applies, since a Unix domain socket address that
// fails any of those could never be a legitimate containerd socket in the
// first place - this alone refuses a class of malformed/injected values
// unconditionally, with no configuration required. When
// PLATFORM_FACTORY_SHIM_ALLOWED_CONTAINERD_SOCKET is set, address must also
// match it exactly: an operator who knows their containerd socket's real,
// fixed location can pin it, so any other value - including a
// structurally valid one - is refused too.
func allowedContainerdSocket(address string) error {
	if address == "" {
		return errors.New("containerd address is empty")
	}
	if strings.ContainsRune(address, 0) {
		return errors.New("containerd address contains a NUL byte")
	}
	if !path.IsAbs(address) {
		return fmt.Errorf("containerd address %q is not an absolute path", address)
	}
	if pinned := strings.TrimSpace(os.Getenv(allowedContainerdSocketEnv)); pinned != "" && address != pinned {
		return fmt.Errorf("containerd address %q does not match the pinned socket %q (%s)", address, pinned, allowedContainerdSocketEnv)
	}
	return nil
}

func (shimManager) Start(ctx context.Context, id string, opts shim.StartOpts) (_ shim.BootstrapParams, retErr error) {
	var params shim.BootstrapParams
	params.Version = 3
	params.Protocol = "ttrpc"

	if err := allowedContainerdSocket(opts.Address); err != nil {
		return params, fmt.Errorf("platform-factory-shim: refusing untrusted containerd address: %w", err)
	}

	grouping := id
	if annotations, err := readBundleAnnotations(); err == nil {
		if sandboxID, ok := annotations[groupLabel]; ok && sandboxID != "" {
			grouping = sandboxID
		}
	}

	address, err := shim.SocketAddress(ctx, opts.Address, grouping, false)
	if err != nil {
		return params, err
	}
	socket, err := shim.NewSocket(address)
	if err != nil {
		if !shim.SocketEaddrinuse(err) {
			return params, fmt.Errorf("platform-factory-shim: create shim socket: %w", err)
		}
		if shim.CanConnect(address) {
			// A shim for this group is already listening; every container
			// in the group joins it instead of starting a second instance.
			params.Address = address
			return params, nil
		}
		if err := shim.RemoveSocket(address); err != nil {
			return params, fmt.Errorf("platform-factory-shim: remove stale shim socket: %w", err)
		}
		if socket, err = shim.NewSocket(address); err != nil {
			return params, fmt.Errorf("platform-factory-shim: recreate shim socket: %w", err)
		}
	}
	// Only unwind (and thereby unlink) the listener on a failure path below.
	// Closing a unix net.Listener removes its socket file from disk, so on
	// success the listener must survive this function: the started child
	// inherits the fd via ExtraFiles and serves on it, while containerd
	// dials the same path back. The parent's copy of the fd is closed by
	// process exit (which does not unlink), matching how
	// containerd-shim-runc-v2's manager keeps its socket alive.
	defer func() {
		if retErr != nil {
			socket.Close()
		}
	}()
	socketFile, err := socket.File()
	if err != nil {
		return params, fmt.Errorf("platform-factory-shim: open shim socket fd: %w", err)
	}
	defer socketFile.Close()

	cmd, err := selfCommand(ctx, id, opts.Address)
	if err != nil {
		return params, err
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, socketFile)
	defer func() {
		if retErr != nil {
			_ = shim.RemoveSocket(address)
		}
	}()
	if err := cmd.Start(); err != nil {
		return params, fmt.Errorf("platform-factory-shim: launch shim process: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = cmd.Process.Kill()
		}
	}()
	go cmd.Wait()

	if err := shim.AdjustOOMScore(cmd.Process.Pid); err != nil {
		return params, fmt.Errorf("platform-factory-shim: adjust shim OOM score: %w", err)
	}
	params.Address = address
	return params, nil
}

func selfCommand(ctx context.Context, id, containerdAddress string) (*exec.Cmd, error) {
	ns, err := namespaces.NamespaceRequired(ctx)
	if err != nil {
		return nil, fmt.Errorf("platform-factory-shim: containerd namespace is required: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("platform-factory-shim: resolve own executable path: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("platform-factory-shim: resolve working directory: %w", err)
	}
	cmd := exec.Command(self, "-namespace", ns, "-id", id, "-address", containerdAddress)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd, nil
}

// readBundleAnnotations reads just the annotations map out of the OCI
// bundle's config.json in the current directory (containerd invokes Start
// with the bundle directory as its working directory), without parsing the
// rest of the spec.
func readBundleAnnotations() (map[string]string, error) {
	var spec struct {
		Annotations map[string]string `json:"annotations"`
	}
	data, err := os.ReadFile("config.json")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, err
	}
	return spec.Annotations, nil
}

// Stop is only ever asked to confirm a shim process is gone; actual
// container teardown already happened through Task.Delete calls (each one
// synchronously deletes its own MicroVM via platform-factory-runtime), and this
// process exits on its own once its last task and sandbox are gone. There is
// nothing left for the manager to reach into from outside that process.
func (shimManager) Stop(ctx context.Context, id string) (shim.StopStatus, error) {
	return shim.StopStatus{ExitStatus: 0, ExitedAt: time.Now()}, nil
}

func (shimManager) Info(ctx context.Context, optionsR io.Reader) (*types.RuntimeInfo, error) {
	return &types.RuntimeInfo{
		Name: runtimeName,
		Version: &types.RuntimeVersion{
			Version:  version.Version,
			Revision: version.Revision,
		},
	}, nil
}

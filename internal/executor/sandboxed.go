package executor

import (
	"context"
	"fmt"
	"net"
	"os/exec"

	"github.com/CYPT71/secure-oci-base/internal/core"
)

// This file holds the host-side glue that only runs when the executor is
// sandboxed: constructing a sandboxed Executor and preparing a stage's
// namespaced command. Exercising it requires the kernel namespace, mount
// and cgroup-v2 privileges that an unprivileged, AppArmor-restricted CI
// runner denies, so it is covered by the dedicated ci-sandbox job (which
// runs under a delegated root scope) rather than by the unit-coverage
// gate. The plain executor and the shared Run dispatch live in
// executor.go and are unit-covered normally.

// NewSandboxed returns an Executor that runs every stage inside fresh
// user, mount, PID, network, IPC and UTS namespaces with the stage root
// as its pivoted filesystem root. mountSources resolves the pipeline
// input IDs referenced by Stage.Mounts to host paths. It fails closed
// when the host cannot create the namespaces; consult support.Details
// for the reason. Binaries using it must call MaybeApplySandboxHelper
// (and MaybeApplyRlimitHelper) at the very start of main().
func NewSandboxed(root string, baseEnv []string, support SandboxSupport, mountSources map[string]string) (*Executor, error) {
	if !support.UserNamespaces {
		return nil, fmt.Errorf("executor: sandbox unavailable: %s", support.Details["user-namespaces"])
	}
	executor := New(root, baseEnv)
	executor.sandboxed = true
	executor.support = support
	executor.mountSources = mountSources
	return executor, nil
}

// prepareSandboxed builds the namespaced command: the stage root becomes
// the filesystem root, network none is enforced by CLONE_NEWNET,
// pids/cpu limits fail closed without cgroup support, and secrets are
// resolved into the payload for in-memory delivery.
func (e *Executor) prepareSandboxed(ctx context.Context, stage core.Stage) (*exec.Cmd, *stageCgroup, net.Conn, [][]byte, error) {
	network := effectiveNetwork(stage.Network)
	if network == core.NetworkResolve && e.dnsForwarder == nil {
		return nil, nil, nil, nil, fmt.Errorf(
			"executor: stage %q requests network policy %q; configure an explicit project-owned DNS forwarder",
			stage.ID, network)
	}
	if network == core.NetworkResolve &&
		(!e.dnsForwarder.GetUpstream().IsValid() || e.dnsForwarder.GetUpstream().Port() == 0) {
		return nil, nil, nil, nil, fmt.Errorf(
			"executor: stage %q requests network policy %q; the DNS forwarder requires an explicit upstream address and port",
			stage.ID, network)
	}
	if stage.Resources.PIDs > 0 && !e.support.CgroupPIDs {
		return nil, nil, nil, nil, fmt.Errorf(
			"executor: stage %q requests a process-count limit; %s", stage.ID, e.supportDetail("cgroup-pids", "cgroup-delegation", "cgroup-v2"))
	}
	if stage.Resources.CPUMilli > 0 && !e.support.CgroupCPU {
		return nil, nil, nil, nil, fmt.Errorf(
			"executor: stage %q requests a CPU limit; %s", stage.ID, e.supportDetail("cgroup-cpu", "cgroup-delegation", "cgroup-v2"))
	}

	payload := sandboxHelperPayload{
		Root:         e.root,
		WorkingDir:   stage.Command.WorkingDir,
		MemoryBytes:  stage.Resources.MemoryMiB << 20,
		ReadOnlyRoot: stage.Sandbox.ReadOnlyRoot,
	}
	for _, mount := range stage.Mounts {
		source, known := e.mountSources[mount.Source]
		if !known {
			return nil, nil, nil, nil, fmt.Errorf(
				"executor: stage %q mounts input %q, which has no resolved host source", stage.ID, mount.Source)
		}
		payload.Mounts = append(payload.Mounts, sandboxBindMount{
			Source: source, Target: mount.Target, ReadOnly: mount.ReadOnly,
		})
	}
	var redactions [][]byte
	for _, secret := range stage.Secrets {
		if e.secrets == nil {
			return nil, nil, nil, nil, fmt.Errorf(
				"executor: stage %q declares secret %q but no secret resolver is configured", stage.ID, secret.ID)
		}
		value, err := e.secrets.Resolve(ctx, secret.ID)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("executor: stage %q secret %q: %w", stage.ID, secret.ID, err)
		}
		payload.Secrets = append(payload.Secrets, sandboxSecretFile{Target: secret.Target, Value: value})
		redactions = append(redactions, append([]byte(nil), value...))
	}

	cmd := exec.CommandContext(ctx, stage.Command.Executable, stage.Command.Args...)
	cmd.Env = e.stageEnv(stage)
	var hostRelay net.Conn
	if network == core.NetworkResolve {
		hostFile, childFile, err := dnsSocketPair()
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("executor: stage %q create DNS relay: %w", stage.ID, err)
		}
		hostRelay, err = net.FileConn(hostFile)
		_ = hostFile.Close()
		if err != nil {
			_ = childFile.Close()
			return nil, nil, nil, nil, fmt.Errorf("executor: stage %q open DNS relay: %w", stage.ID, err)
		}
		cmd.ExtraFiles = append(cmd.ExtraFiles, childFile)
		payload.DNSRelayFD = 3 + len(cmd.ExtraFiles) - 1
		go func() { _ = e.dnsForwarder.ServeRelay(ctx, hostRelay) }()
	}
	if err := wrapWithSandboxHelper(cmd, payload, stage.Sandbox.NonRoot, network != core.NetworkFull); err != nil {
		if hostRelay != nil {
			_ = hostRelay.Close()
		}
		for _, file := range cmd.ExtraFiles {
			_ = file.Close()
		}
		return nil, nil, nil, nil, fmt.Errorf("executor: stage %q: %w", stage.ID, err)
	}
	// The executable is resolved inside the sandbox after pivot_root;
	// a host-side lookup failure recorded by exec.Command is irrelevant.
	cmd.Err = nil

	var group *stageCgroup
	if stage.Resources.PIDs > 0 || stage.Resources.CPUMilli > 0 {
		var err error
		group, err = newStageCgroup(e.support.cgroupDir, cmd, stage.Resources.PIDs, stage.Resources.CPUMilli)
		if err != nil {
			if hostRelay != nil {
				_ = hostRelay.Close()
			}
			for _, file := range cmd.ExtraFiles {
				_ = file.Close()
			}
			return nil, nil, nil, nil, fmt.Errorf("executor: stage %q: %w", stage.ID, err)
		}
	}
	return cmd, group, hostRelay, redactions, nil
}

func (e *Executor) supportDetail(names ...string) string {
	for _, name := range names {
		if detail, ok := e.support.Details[name]; ok {
			return detail
		}
	}
	return "the required cgroup controller is unavailable on this host"
}

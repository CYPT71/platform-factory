# containerd and Kubernetes MicroVM runtime

This integration keeps both execution paths available: ordinary workloads keep
their existing runtime handler, while workloads selecting `platform-factory` execute
the OCI bundle in a native KVM MicroVM. It does not launch a Docker or Podman
container around the VM.

## Why a custom shim, not `io.containerd.runc.v2` + `BinaryName`

An earlier version of this integration pointed containerd's stock
`io.containerd.runc.v2` shim at `platform-factory-runtime` via its `BinaryName`
option, reusing containerd's default "podsandbox" model: the pause container
that anchors each Kubernetes Pod's shared network/IPC namespaces is created
the same way any other container is, from an OCI spec containerd's own CRI
plugin generates.

That model unconditionally assigns the pause container's spec a non-empty
Linux capability set - even with `securityContext.capabilities.drop_capabilities:
["ALL"]` set on the Pod, and even with containerd's `base_runtime_spec`
override loaded (confirmed in containerd's own debug log: the base spec's
`process.capabilities` is present, but is unconditionally overwritten by
containerd's sandbox-generation code afterward). `platform-factory-runtime`
deliberately refuses any OCI spec carrying a non-empty capability set - a
MicroVM's guest kernel does not admit host Linux capabilities into its own
boot process at all, so honoring them would be lying about what protection
they buy. Relaxing that refusal to accommodate containerd's sandbox model was
rejected: the runtime stays strict, and the integration works around
containerd instead.

`platform-factory-shim` (installed as `containerd-shim-platform-factory-v1`, matching
containerd's naming convention for the `io.containerd.platform-factory.v1`
`runtime_type` below) is a full containerd runtime v2 shim implementing both
TTRPC services containerd's `sandboxer = "shim"` model requires:

- a **Sandbox** service that never presents any OCI spec for containerd to
  attach capabilities to in the first place - it does no more than track a
  pod's bundle path, network namespace, and lifecycle timestamps in memory;
  nothing about it needs a capability policy to reject.
- a **Task** service, one per container in the pod, that shells out to the
  same `platform-factory-runtime` CLI Podman already drives in production
  (`create`/`start`/`state`/`kill`/`delete`), so MicroVM lifecycle logic is
  never duplicated.

Every container is still its own independently isolated MicroVM; the shim
adds no shared kernel, VM, or process tree across containers in a pod.

## Installation

On a Linux amd64 KVM node:

```sh
sudo scripts/microvm/install-containerd-runtime.sh install --node worker-01
```

This builds and installs three binaries: `platform-factory-runtime` (the OCI CLI
facade, from the main module), and `containerd-shim-platform-factory-v1` and
`platform-factory-containerd` (from the `plugins/containerd` module - see
"Module layout" below). It then writes the generated containerd config
fragment and RuntimeClass manifest.

The optional `--node` operation is deliberately fail-closed: it labels and
taints the node only after `/dev/kvm` is a readable/writable character device.
The resulting scheduling contract is:

```text
platform-factory.dev/runtime-platform-factory=ready
platform-factory.dev/runtime-platform-factory=ready:NoSchedule
```

Re-running `install` is idempotent. Original files are saved once, replacements
are atomic, and rollback removes only files owned by this installer before
restoring those originals:

```sh
sudo scripts/microvm/install-containerd-runtime.sh probe
sudo scripts/microvm/install-containerd-runtime.sh uninstall --node worker-01
```

containerd must use config version 2 and import the generated fragment:

```toml
version = 2
imports = ["/etc/containerd/conf.d/*.toml"]
```

Restart containerd only after validating the merged configuration:

```sh
sudo containerd config dump
sudo systemctl restart containerd
kubectl apply -f /etc/containerd/conf.d/platform-factory-runtimeclass.yaml
```

Select the VM runtime per Pod:

```yaml
spec:
  runtimeClassName: platform-factory
```

Omit `runtimeClassName` to retain the cluster's normal OCI/container runtime.
`pf deploy` generates the complete scheduling contract when explicitly asked:

```sh
pf deploy --runtime-class platform-factory --dry-run \
  registry.example/team/app@sha256:DIGEST
```

This adds `runtimeClassName`, the matching node selector, and the dedicated
`NoSchedule` toleration. Without the flag, existing ordinary-runtime behavior
is unchanged.

The deployment generator also supports explicit controller shapes while
preserving the same digest pinning, resource requests, pod hardening and
optional RuntimeClass scheduling:

```sh
pf deploy --workload statefulset --dry-run registry.example/team/app@sha256:DIGEST
pf deploy --workload daemonset --dry-run registry.example/team/agent@sha256:DIGEST
pf deploy --workload cronjob --schedule '*/5 * * * *' --dry-run \
  registry.example/team/task@sha256:DIGEST
```

CronJob schedules are restricted to bounded five-field numeric expressions;
named aliases and multi-line input are rejected. Persistent volume claims,
Ingress and application configuration are never synthesized from guesses. They
can be requested explicitly:

```sh
pf deploy --dry-run \
  --ingress-host api.example.com --ingress-path /v1 \
  --config MODE=production \
  --secret-env DATABASE_PASSWORD=database/password \
  --volume /var/lib/api=20Gi \
  registry.example/team/api@sha256:DIGEST
```

`--config KEY=VALUE` creates a ConfigMap and references its keys from the
container. `--secret-env ENV=SECRET/KEY` creates only a `secretKeyRef`: Platform
Factory never reads, copies, logs, or persists the Secret value. `--volume`
creates a `ReadWriteOnce` PVC and matching mount. All options are repeatable;
duplicate environment names and mount paths are rejected instead of being
resolved by ordering. Ingress requires a DNS-safe host and an absolute path.
The generated fragment can be inspected without installing anything:

```sh
go run ./plugins/containerd/cmd/platform-factory-containerd config
go run ./plugins/containerd/cmd/platform-factory-containerd runtimeclass
```

The generated fragment selects `sandboxer = "shim"` (not the default
`"podsandbox"`) and `runtime_type = "io.containerd.platform-factory.v1"` - the two
settings that route pod sandbox creation to `platform-factory-shim` instead of
containerd's default runc-based sandbox model.

## Module layout

`plugins/containerd` is its own Go module (`plugins/containerd/go.mod`),
tied to the main module only through `go.work` for local development and
through `platform-factory-runtime`'s CLI contract at runtime - it does not import
any `internal/` package. This mirrors the project's rule that every
runtime-engine integration beyond the microVM (native) and OCI interfaces
themselves is an out-of-module plugin, and that `internal/` is never a
plugin boundary even where Go's own visibility rule would technically allow
it (a plugin module sharing the repo's import path prefix can still see into
`internal/`): the main module never depends on a plugin, and a plugin only
ever consumes a *public* surface - either the main module's CLI, like
`plugins/containerd` consuming `platform-factory-runtime`, or a stable Go contract
under `api/`, like `plugins/kubevirt` consuming `api/microvm/v1` (`Spec`,
`ValidateCommon`, `Forward`) instead of the `internal/microvm` and
`internal/networking` packages that actually implement the native backend.
`internal/microvm.Spec` and `internal/networking.Forward`/`ParseForward` are
now aliases of their `api/microvm/v1` originals for exactly this reason: one
definition, consumed identically whether the caller is in-module or a
plugin. `plugins/kubevirt` follows the same module-level shape as
`plugins/containerd`, but through a different, and more thoroughly wired,
kind of public surface: `plugins/kubevirt/cmd/platform-factory-kubevirt` is
a real out-of-process plugin speaking the `sdk/plugin` wire protocol (the
same one language-detection plugins use), not a binary the main module
`exec`'s directly by name. `platform-factory microvm --backend=kubevirt`
discovers, verifies and starts it through `internal/plugin.Registry`, the
same declared->discovered->negotiated->verified->available lifecycle every
other plugin capability goes through, and dispatches by capability
(`runtime.create`, `runtime.start`, `runtime.stop`, `runtime.restart`,
`runtime.status`, `runtime.logs`, `runtime.delete`, `runtime.rbac`) rather
than by a hardcoded binary name. `--backend=kubevirt` therefore now
requires an installed, trusted kubevirt plugin directory (`--plugin-dir`,
optionally `--plugin-key` for a signed manifest, or
`--allow-unverified-plugin` to accept an unsigned one) - it no longer
falls back to silently `exec`ing `platform-factory-kubevirt` off `$PATH`.
Because kubectl/virtctl genuinely need to reach a real Kubernetes API
server and read a kubeconfig, the plugin's manifest declares
`permissions.network` and `permissions.secrets: ["kubeconfig"]`; the host
sandbox (`internal/plugin/sandbox_linux.go`'s `hostNetworkGranted` and
`declaresKubeconfigSecret`) grants real host network access and
KUBECONFIG/HOME passthrough only because - and only when - the plugin's
own signed manifest asks for them, not by default the way every other
plugin family gets an isolated, connectivity-less network namespace and a
stripped environment. `plugins/containerd`'s shim cannot follow this same
pattern: `platform-factory-shim` is invoked by containerd itself as a
containerd shim, under containerd's own process supervision and wire
protocol (`containerd-shim-platform-factory-v1`), not by
`platform-factory`, so there is no equivalent host-side dispatch point to
route through `internal/plugin.Registry`.

## CI coverage

CI validates this contract on a kind cluster containing one control-plane and
two workers. Two ordinary Pods become Ready across distinct workers. Two Pods
selecting `platform-factory` are also scheduled across distinct workers, but fail
closed at sandbox creation because the disposable kind nodes deliberately have
neither the runtime installed nor `/dev/kvm`. This proves CRI handler selection
and scheduler behavior; it is not reported as a MicroVM boot test. Native KVM
boot remains covered separately by the MicroVM jobs, and the shim's own
Sandbox/Task TTRPC contract is covered by `plugins/containerd`'s unit tests
plus a local (non-CI) end-to-end validation against a real containerd and
`crictl`.

A separate multi-node job stops one kind worker container after placing two
ordinary-runtime replicas across both workers. Kubernetes must mark the lost
node NotReady, evict its Pod under an explicit five-second toleration and
restore both replicas on the surviving worker. This proves control-plane
scheduling and worker-loss recovery, not MicroVM migration or restart.

## Current boundary

Image pulling and OCI bundle construction remain containerd/CRI duties. The
secure runtime accepts the strict OCI subset documented by
`platform-factory-runtime features`; host bind mounts, terminals, hooks, cgroups
and Linux namespace requests fail closed - the shim does not, and cannot,
loosen that policy on the runtime's behalf. `platform-factory-shim`'s Task service
does not implement exec, pause/resume, checkpoint, or stats: a MicroVM-backed
container is not a process tree those operations apply to, and it fails them
closed rather than silently no-op.

## Socket authorization

`platform-factory-shim` is invoked by containerd itself as a containerd shim
(`containerd-shim-platform-factory-v1`), under containerd's own process
supervision and wire protocol - not by `platform-factory`, so it cannot go
through `internal/plugin.Registry`'s discover/verify/sandbox lifecycle the
way the KubeVirt plugin does. The one enforcement point this codebase does
control is `shimManager.Start`
(`plugins/containerd/cmd/platform-factory-shim/manager.go`), which receives
containerd's own daemon socket address (`opts.Address`) and, until this
hardening, forwarded it unvalidated to the per-container TTRPC process it
spawns. `allowedContainerdSocket` now refuses it unconditionally unless it
is a well-formed absolute Unix-domain-socket path (never empty, no NUL
byte), and - when the operator sets
`PLATFORM_FACTORY_SHIM_ALLOWED_CONTAINERD_SOCKET` - requires an exact match
against that pinned value, refusing every other address including
structurally valid ones. This is the "socket explicitly authorized" tier
the wiki's Definition of Done names for containerd, applied at the only
point in this shim's own code that ever observes the address before using
it.

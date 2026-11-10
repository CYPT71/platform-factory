# Architecture and OCI layout

## Source map

The repository separates public contracts, application orchestration and
implementation packages. The main CLI composes those layers; integrations
and language plugins are separate Go modules listed in `go.work`.

| Responsibility | Source | Documentation |
| --- | --- | --- |
| CLI flags, interactive flows and dependency wiring | `cmd/platform-factory`, `cmd/tui` | [CLI guide](../cli-guide.md) |
| Build, publication, verification, project initialization and migration use cases | `internal/app` | [API compatibility](../api-compatibility.md) |
| Canonical pipeline and migration models | `internal/core`, `internal/migration` | [Migration orchestration](../../internal/app/migration/README.md) |
| Versioned public DTOs and SDK adapters | `api/*/v1`, `sdk` | [API compatibility](../api-compatibility.md) |
| Pipeline decoding, scheduling, stage execution and cache | `internal/pipeline`, `internal/scheduler`, `internal/executor`, `internal/cache` | [Distributed builds](../distributed-builds.md) |
| OCI assembly, validation and rootfs extraction | `oci`, `internal/assemble`, `internal/layout`, `internal/rootfs` | [Layout below](#oci-image-layout) |
| Registry, SBOM, provenance, signatures and policy | `internal/registry`, `internal/sbom`, `internal/provenance`, `internal/signing`, `internal/policy` | [Security model](../security/model.md) |
| Durable operation claims and workload recovery | `internal/idempotency`, `internal/workloadstate`, `internal/core/statemachine.go` | [Publication recovery](../cli-guide.md#recovering-an-interrupted-publication) |
| Language inspection, freezing and runtime layers | `sdk/langplugin`, `plugins/lang-*`, `internal/app/provisionruntime` | [Language plugins](../language-plugin-layers.md) |
| Capability-based plugin RPC and installation | `internal/plugin`, `sdk/plugin`, `internal/marketplace` | [Plugin bundles](../plugin-bundles.md), [marketplace](../plugin-marketplace.md) |
| Native container and microVM execution | `internal/ociruntime`, `internal/hypervisor`, `internal/runtime`, `internal/guest`, `internal/guesttransport` | [Runtime integration](../containerd-kubernetes.md) |
| Legacy disk inspection and boot selection | `internal/vmdisk` | [Legacy VM boot](../legacy-vm-disk-boot.md) |
| Distributed leases, worker execution and mTLS | `cmd/platform-factory-control-plane`, `cmd/platform-factory-worker`, `internal/app/workerpipeline`, `internal/mtls` | [Distributed builds](../distributed-builds.md) |
| MCP transport and tool adapters | `internal/mcp` | [MCP server](../mcp.md) |
| Offline analyzers, hooks and opt-in remediation | `internal/extensions`, `internal/hooks` | [Technology toolkit](../technology-toolkit.md) |

`internal/archtest` checks dependency boundaries across the workspace. Public
`api` and `sdk` packages must not import `internal`; internal packages must
not import public adapters or command composition roots. Plugin modules may
not import host internals or other plugin modules. Migration orchestration
uses inward-facing ports rather than importing the concrete plugin registry.
These rules are narrower than a claim that every package is fully independent;
the executable checks are the reference for the enforced boundaries.

## OCI image layout

The CLI validates an executable, creates a deterministic `tar` layer and
gzip stream, then writes the OCI image configuration, manifest, index,
and content-addressed blobs. A generated layout has this shape:

```text
oci-image/
  oci-layout                 # imageLayoutVersion 1.0.0
  index.json                 # platform descriptor and image reference annotation
  blobs/sha256/
    <config digest>
    <manifest digest>
    <compressed layer digest>
```

With the default entrypoint, the layer contains `/app/service`, `/etc/ssl/certs/`, `/tmp/`, and
`/var/tmp/`, plus one entry per `-extra-file` if any were given. The last
two directories are sticky writable directories; all other included
directories are `0755`, and every file (the entrypoint and every extra
file) defaults to `0555`. The builder's options allow explicit file modes
and opt-in binary ownership preservation. The config specifies `linux`,
`amd64` or `arm64`, each layer's uncompressed `diff_id`, and a default
`User` of `65532:65532`.

The default build emits one generated compressed layer. With semantic layers
enabled, files are grouped in deterministic `toolchain`, `dependencies`,
`application`, `metadata` order, omitting empty groups. Explicit plugin-owned
`ExtraLayers` are validated and appended separately; neither the verifier nor
the manifest contract assumes that every image has only one layer.

See also [`internal/layout`](../../internal/layout) for the verifier that
reads this shape back, and the
[compatibility matrix](../reference/compatibility.md) for which
OS/architecture/runtime combinations actually consume it in CI.

## Scheduling and guest-device scope

Decision for the current local reliability work, 2026-11-10: keep distributed
lease scheduling in the control-plane process, and keep stage scheduling in
`internal/scheduler`. Do not add a third scheduler executable or additional
virtio devices to complete a feature checklist. This fixes the current scope;
it does not qualify the existing distributed or hypervisor backends as Stable.

The [control-plane startup](../../cmd/platform-factory-control-plane/main.go)
already restores lease state and quota usage before serving requests. The
[server](../../cmd/platform-factory-control-plane/server.go) owns admission and
lease transitions. Separating the scheduler now would introduce another
identity, protocol, deployment unit and cross-process recovery boundary. The
measured local packaging/FastAPI journeys exercise neither that process split
nor a guest device; they provide no evidence that such an expansion would
remove a user blocker.

| Alternative | Consequence and current decision |
| --- | --- |
| Keep lease scheduling with the control plane | Retains the existing snapshot/admission boundary and two distributed executables. It couples scheduler lifetime and scaling to the control plane; this constraint remains explicit. Selected for the current scope. |
| Extract a scheduler executable now | Could allow independent deployment or scaling, but requires a versioned lease ownership/leadership protocol, authentication, idempotent recovery and partition tests. Deferred until a measured case needs independent scaling or failure isolation. |
| Add generic virtio device helpers now | Adds code and advertised surface without proving a bootable device/backend combination. Deferred until a named guest workload requires it. |
| Extend a backend for a concrete guest requirement | Requires a device contract, feature negotiation, queues, bounds checks, reset/interrupt handling and boot/I/O/recovery tests on the native target. This is the route for any future device work. |

The [virtio package](../../internal/hypervisor/virtio) contains minimal helpers.
Its existence is not a promise that every backend implements each device.
KVM queues and block/network handling belong to the concrete
[KVM backend](../../internal/hypervisor/kvm); HVF uses its native adapter. Keep
those implementation boundaries and their individual maturity levels.

Reopen this decision when a recorded workload demonstrates a scheduler
capacity/failure-isolation limit, or a guest cannot perform an essential task
with the existing devices. Before implementation, record the triggering
workload, expected observable behavior, responsible maintainer, supported
platform and acceptance/failure cases. No new executable or device is selected
by this decision, so no unimplemented contract is advertised as delivered.

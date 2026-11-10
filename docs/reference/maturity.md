# Maturity matrix

Stabilisation roadmap item 4: a maturity label per capability, published
separately from narrative docs so "is X safe to depend on" has one place
to check. Labels:

- **Stable** — implemented, tested (positive and negative cases), CI-gated
  on a real execution path, and not expected to change shape between
  releases without a migration.
- **Beta** — implemented and CI-tested end to end, but the interface or
  wire format may still change between releases (README calls these out
  explicitly as "experimental formats").
- **Alpha** — implemented and unit-tested in isolation, but not yet wired
  into a real execution path used by the shipped CLI (library-only).
- **Stub** — the interface/type exists as a deliberate extension point;
  no backend implementation exists or is planned without the point being
  explicitly reopened.
- **Out of scope** — will not be implemented (a scope decision, not a gap).

Original maturity assessment: 2026-08-04. Implementation notes corrected
against the source on 2026-11-05; this does not establish new release or
hardware acceptance evidence. Re-verify by source inspection, build and tests
before relying on a label.

## Core OCI (v1)

| Capability | Maturity | Notes |
| --- | --- | --- |
| `build`/`verify`/`inspect`/`diff`/`compose` | Stable | Deterministic layout construction and verification; reproducible-rebuild proof via `--rebuild=N --require-identical` |
| `sbom` | Stable | Native inventory, no external tool |
| Project config schema (`pf.yaml` v1; optional long name `platform-factory.yaml`) | Beta | `pf init` writes one naming style and its adjacent lock; ambiguous configurations fail closed. New locks are v2, with v1 retained for migration. See [project-file compatibility](../api-compatibility.md#project-files). Legacy `.config_image.*` spellings remain discovery inputs. |
| Project plan JSON (`platform-factory.dev/project-plan/v1`) | Beta | Same README caveat |
| Freeze inventory (`.platform-factory/freeze.lock.json`) | Beta | Same README caveat |
| Structured JSONL build events | Beta | Same README caveat |
| Semantic layers (`--semantic-layers`) | Beta | Opt-in precisely because enabling it changes layer digests |

## Pipeline and plugins (v2)

| Capability | Maturity | Notes |
| --- | --- | --- |
| Pipeline DAG (`api/pipeline/v1`, `internal/pipeline`) plan/run/scheduler | Stable | Deterministic ordering, canonical fingerprint, budget enforcement, real conformance suite |
| Sandboxed stage executor (Linux namespaces/cgroups) | Stable on Linux | Fails closed where the host can't provide isolation; not sandboxed on other hosts (falls back to the minimal executor, which itself refuses secrets/policies) |
| Plugin protocol + Go/Python/JS/TS/C# SDKs | Stable | 5/5 languages pass the same `platform-factory-conformance plugin` suite (re-verified 2026-08-04, see the implementation roadmap) |
| Plugin process sandbox (namespaces, rlimits) | Beta | Linux namespace isolation is required by default; unavailable or failed sandbox setup returns an error unless the trust policy explicitly permits unsandboxed execution. Family profiles in `internal/plugin/permission_profile.go` request memory, CPU-time and process limits (failed `Setrlimit` calls are logged); an unknown family retains CPU/process limits without a memory ceiling. These RPC-plugin rules do not describe the separate plain-executable language-plugin interface. |

## Supply chain (v3)

| Capability | Maturity | Notes |
| --- | --- | --- |
| Native Registry client — push path (push, tag, artifact push) | Stable | See `internal/registry` |
| Native Registry client — pull path (`GetManifest`, `GetBlob`) | Alpha | Digest-verified downloads are wired into `pf plugin provision-runtime` through `internal/app/provisionruntime/pull.go`, including Linux platform selection and shared safe rootfs extraction. There is no general-purpose pull command; this label does not claim release-wide pull interoperability. |
| Native SBOM/provenance/signing (Ed25519/ECDSA, DSSE) | Stable | Replaced Cosign as the primary path; Cosign retained only as a CI interoperability cross-check |
| Native publication policy (`internal/policy`) | Stable | `platform-factory publish`/`verify-release` both gate on it |
| `platform-factory verify-release` | Beta | New 2026-08-04; real cryptographic/schema checks, but the command surface itself hasn't had a release cycle yet to prove its flags are the right shape |
| Chunked CAS / resumable uploads | Stable | Exercised against a simulated 2 TiB logical bench without requiring 2 TiB of disk |

## MicroVM (v4)

| Capability | Maturity | Notes |
| --- | --- | --- |
| KVM (Linux/amd64) | Stable | Boots a real kernel against real hardware in CI, exercised through Podman/Docker/containerd |
| HVF (macOS/arm64) | Beta | Real ARM64 boot and Rosetta guest execution verified on an entitled local Mac. Hosted macOS runners can exercise fallback contracts without exposing virtualization; a green hosted job alone is not native boot evidence. No multi-runtime exercise comparable to KVM |
| KVM (Linux/arm64) | Unsupported direct boot | The shipped direct-boot backend is Linux/amd64; ARM64 guests need a macOS/arm64 HVF runner. |
| WHPX (Windows) | Alpha | `internal/hypervisor/whpx` implements an availability probe only (`WHvGetCapability`), not a guest lifecycle backend; real Windows implementation and execution acceptance are still required |
| `internal/hypervisor/virtio` | Alpha | Minimal configuration and console helpers. This package's status does not describe all guest devices: KVM's actual queues and block/network device handling live under `internal/hypervisor/kvm`, while HVF uses its native framework adapter. |
| `internal/hypervisor/sandbox` (seccomp/cgroups/privilege-drop for the VMM host process) | Beta | Wired into `cmd/platform-factory-runtime`: `no_new_privs` and strict classic-BPF seccomp are applied; PID/IPC/UTS namespaces, cgroup limits, and current plus bounding capability drops are probe-gated (skip cleanly on a host without the required privilege). Mount/network namespaces remain open while host rootfs/TAP resources are opened after confinement. Linux CI validates the namespaced lifecycle. See Threat Model T22. |
| Firecracker / Cloud Hypervisor / libkrun / Kata backends | Out of scope | Explicit scope decision: keep the `MicroVMBackend` interface as an extension point, do not implement any of the four |
| MicroVM as a Windows/macOS *image target* (not host) | Out of scope | The stable builder's scope is Linux `amd64`/`arm64` images only |

## Distributed platform (v5)

| Capability | Maturity | Notes |
| --- | --- | --- |
| `platform-factory-control-plane` / `platform-factory-worker` | Beta | Real mTLS with role-checked certificates, durable leases and real inline pipeline execution under a confined worker root; exercised in `ci-kind-multinode.yml` (distributed cancellation, worker-loss recovery). Worker CAS fetching and materialization are available with opt-in control-plane `-cas-dir`; mTLS clients share that store, without per-tenant storage isolation. Open-lease quota counters are rebuilt from durable state before serving. Arbitrary source acquisition and HA are not implied. See [distributed builds](../distributed-builds.md). |
| `internal/quota` (per-tenant resource quotas) | Beta | Wired into the control-plane, but opt-in — a deployment that doesn't enable it gets none of it |
| `internal/budget` (wall-clock/CPU budget) | Stable for its wired scope | Deliberately wired into `oci.Build` (single-process), not `internal/executor` (would have measured the wrong process — see the roadmap's own audit) |
| `internal/observability` (structured logs/metrics/traces) | Alpha | Structured logging is used by the control plane; the worker and scheduler create spans, while migration workflows carry trace IDs into evidence. The in-process tracing and metrics APIs do not by themselves establish an external telemetry export or production monitoring contract. |
| `internal/errors` (typed error model) | Alpha | Used by `internal/policy`, `internal/scheduler` and `cmd/platform-factory-worker`; this remains a partial adoption rather than a shared contract enforced across every component. |
| mTLS component authentication, scheduling by platform/capability/load/cache-locality, idempotent lease/event/cancellation distribution, worker-loss build resumption, CAS replication with verify-before-accept, workload-identity provenance signing | Beta | Each checked `[x]` under v5's own exit criteria in the implementation roadmap as of 2026-08-04, but new enough this cycle that this doc doesn't call them Stable yet (no cross-release compatibility history) |
| Per-tenant quotas/priorities/fairness | Alpha | The v5 exit criterion itself ("quotas, priorités, fairness et limites par tenant") is still unchecked; `internal/quota` exists and is wired opt-in into the control-plane, but not to the full bar the milestone sets |
| Separate control-plane and worker binaries | Alpha | Lease scheduling remains inside the control plane. A third scheduler executable is deferred by the [current scope decision](../architecture/oci-layout.md#scheduling-and-guest-device-scope); this does not qualify multi-node recovery or isolation |

## Third-party review status (separate axis, not a maturity label)

The items below are well self-tested but have had no independent review,
per the stabilisation roadmap's own "15 actions" audit — see the Threat
Model's "Open work" section for specifics:

- OCI extraction (`internal/rootfs`)
- Sandbox (`internal/executor`, `internal/hypervisor/sandbox`)
- Cryptography and the plugin protocol (`internal/signing`, plugin RPC)

Self-testing is not a maturity level here; it's a distinct, unresolved
gap that applies across several of the "Stable" rows above.

## Active stabilisation work

The [stabilisation record](../stabilisation.md) tracks the current corrections,
local execution evidence and remaining gates for every non-stable row. Passing
local tests does not automatically promote these labels. New language inspection
responses carry a versioned contract; this does not certify every language's
package manager, runtime or cross-release lifecycle.

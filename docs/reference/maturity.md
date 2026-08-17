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

Reviewed 2026-08-04. Re-verify by grep/build/test before trusting a label
that looks stale — this file decays exactly like the roadmap docs it's
summarized from.

## Core OCI (v1)

| Capability | Maturity | Notes |
| --- | --- | --- |
| `build`/`verify`/`inspect`/`diff`/`compose` | Stable | Deterministic layout construction and verification; reproducible-rebuild proof via `--rebuild=N --require-identical` |
| `sbom` | Stable | Native inventory, no external tool |
| Project config schema (`.config_image.yaml` v1) | Beta | README: "remains experimental in v1 and may change between releases"; `platform-factory project migrate` exists specifically because of this |
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
| Plugin process sandbox (namespaces, rlimits) | Beta | Real isolation on Linux with a user namespace; falls back to unsandboxed elsewhere; no per-language memory limit (`RLIMIT_AS` was tried and reverted, see the roadmap) |

## Supply chain (v3)

| Capability | Maturity | Notes |
| --- | --- | --- |
| Native Registry client — push path (push, tag, artifact push) | Stable | See `internal/registry` |
| Native Registry client — pull path (`GetManifest`, `GetBlob`) | Alpha | Added 2026-08-04, digest-verified, tested against a real local `registry:2` in addition to mocked unit tests — but nothing in the shipped CLI consumes it yet (no `platform-factory pull`; `verify-release` remains local-artifact-only by design) |
| Native SBOM/provenance/signing (Ed25519/ECDSA, DSSE) | Stable | Replaced Cosign as the primary path; Cosign retained only as a CI interoperability cross-check |
| Native publication policy (`internal/policy`) | Stable | `platform-factory publish`/`verify-release` both gate on it |
| `platform-factory verify-release` | Beta | New 2026-08-04; real cryptographic/schema checks, but the command surface itself hasn't had a release cycle yet to prove its flags are the right shape |
| Chunked CAS / resumable uploads | Stable | Exercised against a simulated 2 TiB logical bench without requiring 2 TiB of disk |

## MicroVM (v4)

| Capability | Maturity | Notes |
| --- | --- | --- |
| KVM (Linux/amd64) | Stable | Boots a real kernel against real hardware in CI, exercised through Podman/Docker/containerd |
| HVF (macOS/arm64) | Beta | Boots a real kernel against real hardware in CI, but only one job, no multi-runtime exercise the way KVM gets |
| KVM (Linux/arm64) | Alpha | Code exists (`internal/hypervisor/kvm` has amd64-specific boot files); not CI-tested |
| WHPX (Windows) | Alpha | Code exists (`internal/hypervisor/whpx`); not CI-tested; tracked in the roadmap as "non-blocking" |
| `internal/hypervisor/virtio` | Alpha | Configuration stubs, no real virtio protocol implementation yet (per the implementation roadmap's 2026-08-03 audit) |
| `internal/hypervisor/sandbox` (seccomp/cgroups/privilege-drop for the VMM host process) | Beta | Wired into `cmd/platform-factory-runtime`: `no_new_privs` and strict classic-BPF seccomp are applied; PID/IPC/UTS namespaces, cgroup limits, and current plus bounding capability drops are probe-gated (skip cleanly on a host without the required privilege). Mount/network namespaces remain open while host rootfs/TAP resources are opened after confinement. Linux CI validates the namespaced lifecycle. See Threat Model T22. |
| Firecracker / Cloud Hypervisor / libkrun / Kata backends | Out of scope | Explicit scope decision: keep the `MicroVMBackend` interface as an extension point, do not implement any of the four |
| MicroVM as a Windows/macOS *image target* (not host) | Out of scope | The stable builder's scope is Linux `amd64`/`arm64` images only |

## Distributed platform (v5)

| Capability | Maturity | Notes |
| --- | --- | --- |
| `platform-factory-control-plane` / `platform-factory-worker` | Beta | Real mTLS with role-checked certificates, durable leases and real inline pipeline execution under a confined worker root; exercised in `ci-kind-multinode.yml` (distributed cancellation, worker-loss recovery). CAS source materialization and HA remain incomplete. |
| `internal/quota` (per-tenant resource quotas) | Beta | Wired into the control-plane, but opt-in — a deployment that doesn't enable it gets none of it |
| `internal/budget` (wall-clock/CPU budget) | Stable for its wired scope | Deliberately wired into `internal/oci.Build` (single-process), not `internal/executor` (would have measured the wrong process — see the roadmap's own audit) |
| `internal/observability` (structured logs/metrics/traces) | Alpha | Logging is wired into `platform-factory-control-plane`; metrics and distributed tracing remain unused by any real path; `platform-factory-worker` and the OCI builder don't import it |
| `internal/errors` (typed error model) | Alpha | One committed consumer (`internal/policy`); a second (`internal/scheduler`) was in progress, uncommitted, as of this writing — not yet "common to all components" as originally scoped |
| mTLS component authentication, scheduling by platform/capability/load/cache-locality, idempotent lease/event/cancellation distribution, worker-loss build resumption, CAS replication with verify-before-accept, workload-identity provenance signing | Beta | Each checked `[x]` under v5's own exit criteria in the implementation roadmap as of 2026-08-04, but new enough this cycle that this doc doesn't call them Stable yet (no cross-release compatibility history) |
| Per-tenant quotas/priorities/fairness | Alpha | The v5 exit criterion itself ("quotas, priorités, fairness et limites par tenant") is still unchecked; `internal/quota` exists and is wired opt-in into the control-plane, but not to the full bar the milestone sets |
| Separate control-plane/scheduler/worker binaries | Alpha | Explicitly unchecked in the roadmap; scheduler logic is not yet split into its own binary |

## Third-party review status (separate axis, not a maturity label)

The items below are well self-tested but have had no independent review,
per the stabilisation roadmap's own "15 actions" audit — see the Threat
Model's "Open work" section for specifics:

- OCI extraction (`internal/rootfs`)
- Sandbox (`internal/executor`, `internal/hypervisor/sandbox`)
- Cryptography and the plugin protocol (`internal/signing`, plugin RPC)

Self-testing is not a maturity level here; it's a distinct, unresolved
gap that applies across several of the "Stable" rows above.

# Component maturity

This document records the release-scope assessment using Experimental,
Supported and Stable support levels. The [capability maturity matrix](reference/maturity.md)
separately records implementation readiness using Stable, Beta, Alpha and Stub.
The two scales answer different questions; an implemented capability does not
automatically gain production support or independent security review.

This document describes the current, honest state. It is not itself an
enforcement mechanism: nothing in the CLI currently reads or enforces
these levels (see "What isn't true yet" at the bottom).

## Levels

### Experimental

- API may change without notice.
- Tests are partial.
- No compatibility guarantee across versions.
- Development use only.

### Supported

- Automated tests exist and are exercised in CI.
- Documented.
- Errors are clean and actionable, not raw panics/stack traces.
- Migration between versions is reasonable.
- Used in at least one real scenario (an actual project, not only unit
  tests).
- Known limitations are written down somewhere findable.

### Stable

- Versioned API.
- End-to-end tests.
- Failure-injection tests (kill/restart/partial-failure scenarios).
- Documented compatibility policy (see `docs/api-compatibility.md`).
- Rollback path.
- Observability (structured logs/events at minimum).
- Critical boundaries have been audited.
- A defined operational support posture (who to page, what the runbook
  is).

## Classification

| Component | Level | Why |
|---|---|---|
| OCI builder (`oci`, `cmd/oci-builder`) | **Supported** | Deterministic-build and hostile-layout regression tests in CI (`ci-quality.yml`), with the versioned `api/oci/v1` adapter and [compatibility policy](api-compatibility.md). This support level does not establish independent security review or every failure-injection/operational requirement of Stable. |
| Supply chain (SBOM, provenance, signing, `internal/sbom`, `internal/provenance`, `internal/signing`) | **Supported** | Exercised by the publication/evidence paths and their tests; a plain low-level build does not automatically emit all evidence. Independent security review remains a separate requirement. |
| Language plugins - Go, Python, Node.js (`internal/app/project` built-in freeze adapters, `plugins/lang-python`, `plugins/lang-node`) | **Supported** | The three officially frozen languages for the next release (see "Scope freeze" below); tested, documented in `docs/language-plugin-layers.md`. |
| Language plugins - Ruby, PHP, Java, .NET, Rust (`plugins/lang-*`) | **Experimental** | Built and tested in isolation same as Python/Node, but outside the frozen release scope below - API/behavior may still shift. |
| `pf plugin load/unload/list` (`sdk/langplugin`, `cmd/platform-factory/plugin.go`) | **Experimental** | Managed binary installation, inspection probing and source preparation have tests; that evidence alone does not establish a cross-release support guarantee. |
| containerd runtime (`plugins/containerd`) | **Supported** | Own test suite, used by the kind-cluster CI job; no failure-injection tests (containerd restart mid-operation, etc.) yet. |
| KubeVirt deployment (`plugins/kubevirt`) | **Experimental** | Tested in isolation; RBAC/permission model and real-cluster failure modes not yet exercised end to end. |
| Legacy VM disk boot (`internal/vmdisk`, `pf init` legacy-disk detection) | **Experimental** | Recently added (`Started Meine Graal v6.1`, commit `0e24a0d`); format coverage (qcow2/vhd/vhdx) is real but no production usage yet. |
| Distributed control plane (`cmd/platform-factory-control-plane`, `cmd/platform-factory-worker`) | **Beta** | Durable leases, mTLS roles, quotas, signed completions and real pipeline stages are implemented; kind validates cancellation and worker-loss recovery. Worker CAS transfer can be enabled with `-cas-dir`; durable open-lease quota accounting is restored at startup. HA and multi-version rolling upgrades remain open. |
| Cross-platform VMM (HVF on macOS, KVM on Linux) | **Supported on native Linux/KVM and native macOS/HVF; Experimental elsewhere** | Native KVM direct boot targets Linux/amd64; HVF targets macOS/arm64. The CLI cross-builds for Linux/arm64, but this does not provide an ARM64 KVM backend: strict host seccomp also fails closed outside Linux/amd64. See the [compatibility matrix](reference/compatibility.md) for hardware and CI evidence. |

## Scope freeze for the next release

Per item 4: the next stable release should not ship the entire vision at
once. The frozen path is:

```text
local modern source
  -> pf init
  -> pf build
  -> reproducible OCI
  -> SBOM / provenance / signature
  -> registry publish
  -> Kubernetes container deployment
```

Officially supported languages for that path: **Go, Python, Node.js**.
Ruby/PHP/Java/.NET/Rust language plugins, KubeVirt and legacy VM disk boot
remain outside that frozen support scope. The distributed control plane has
the separate Beta implementation assessment above; that label does not add it
to the supported release path.

## What isn't true yet

Being honest about the gap between this document and enforcement, per
the same "moins mensonger" principle it's built on:

- There is no `experimental: true` field in `platform-factory.yaml` and
  no code path that reads one. A project using an Experimental component
  today gets no runtime warning that it's outside the frozen/supported
  scope - this table is the only place that's currently written down.
- No CI job checks that a component's actual test coverage matches the
  level claimed here. This is a snapshot, not a gate - it will drift
  unless someone (or some future automation) re-verifies it.
- Ownership: this is presently a single-maintainer project (see git
  history), so a multi-person ownership matrix does not yet apply in the
  traditional sense. Every domain listed above is currently the
  responsibility of whoever is working on this repo at the time - the
  value of naming that explicitly, per item 31, is only to prevent any
  one area from quietly becoming "everyone assumes someone else
  understands this," not to assign blame.

## Stabilisation in progress

See the [execution record](stabilisation.md) for corrections and remaining
acceptance gates. The requested target is Stable for all experimental features;
that target has not been reached by merely changing this table.

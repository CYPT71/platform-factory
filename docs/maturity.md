# Component maturity

Per `Sanetizer-todo.md` phase 2 (items 3, 4, 31): a plain boolean
stable/unstable lies by omission. Every component below is classified
against one of three levels, with the criteria that earned it that level
- not a vibe, a checklist.

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
| OCI builder (`internal/oci`, `cmd/oci-builder`) | **Supported** | Deterministic-build and hostile-layout regression tests in CI (`ci-quality.yml`); no versioned compatibility policy or failure-injection tests yet, so not Stable. |
| Supply chain (SBOM, provenance, signing, `internal/sbom`, `internal/provenance`, `internal/signing`) | **Supported** | Tested, used by every build; no dedicated threat-model audit performed yet. |
| Language plugins - Go, Python, Node.js (`internal/project` built-in freeze adapters, `plugins/lang-python`, `plugins/lang-node`) | **Supported** | The three officially frozen languages for the next release (see "Scope freeze" below); tested, documented in `docs/language-plugin-layers.md`. |
| Language plugins - Ruby, PHP, Java, .NET, Rust (`plugins/lang-*`) | **Experimental** | Built and tested in isolation same as Python/Node, but outside the frozen release scope below - API/behavior may still shift. |
| `pf plugin load/unload/list` (`sdk/langplugin`, `cmd/platform-factory/plugin.go`) | **Experimental** | New this week (2026-08-06); no real-scenario usage yet beyond this session's own smoke tests. |
| containerd runtime (`plugins/containerd`) | **Supported** | Own test suite, used by the kind-cluster CI job; no failure-injection tests (containerd restart mid-operation, etc.) yet. |
| KubeVirt deployment (`plugins/kubevirt`) | **Experimental** | Tested in isolation; RBAC/permission model and real-cluster failure modes not yet exercised end to end. |
| Legacy VM disk boot (`internal/vmdisk`, `pf init` legacy-disk detection) | **Experimental** | Recently added (`Started Meine Graal v6.1`, commit `0e24a0d`); format coverage (qcow2/vhd/vhdx) is real but no production usage yet. |
| Distributed control plane (`cmd/platform-factory-control-plane`, `cmd/platform-factory-worker`) | **Beta** | Durable leases, mTLS roles, quotas, signed completions and real pipeline stages are implemented; kind validates cancellation and worker-loss recovery. CAS transfer, HA and multi-version rolling upgrades remain open. |
| Cross-platform VMM (HVF on macOS, KVM on Linux) | **Supported on native Linux/KVM and native macOS/HVF; Experimental elsewhere** | `linux/amd64` and `darwin` (native) are both exercised for real in CI and in this session's own testing. `linux/arm64` currently does not even compile (`internal/hypervisor/sandbox`'s seccomp table is x86_64-only - see the "Known gap" note in `containers/dev/Dockerfile`), so it is not merely Experimental there, it is broken; treat it as such until that's fixed. |

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
Everything else (Ruby/PHP/Java/.NET/Rust language plugins, KubeVirt,
legacy VM disk boot, the distributed control plane) remains available
but Experimental, per the table above.

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

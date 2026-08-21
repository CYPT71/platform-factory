# Compatibility matrix

Every row below is backed by a named CI workflow that actually exercises
it — not a claim inferred from "should work." Where nothing exercises a
combination, it is marked absent rather than assumed. Reviewed 2026-08-04
against `.github/workflows/` as it exists on `main`; re-check this file
whenever a workflow's matrix, runner image, or pinned version changes (see
the review trigger at the bottom).

## Host OS (running the `platform-factory` CLI / builder)

The builder and layout verifier are pure Go with no external process
dependency, so this axis is about the *host running `platform-factory`*, not the
Linux target OS a built image runs on (see the next section).

| Host OS | Status | Evidence |
| --- | --- | --- |
| Linux (ubuntu-24.04) | ✅ tested | `ci-reproducibility.yml` matrix, `ci-quality.yml`, most other workflows |
| macOS (macos-15) | ✅ tested | `ci-reproducibility.yml` matrix; also verifies the local install includes the native VMM |
| Windows (windows-2025) | ✅ tested | `ci-reproducibility.yml` matrix |

`scripts/local/bootstrap.sh --target OS/ARCH` cross-builds for
`linux\|darwin\|windows` × `amd64\|arm64` from any POSIX host, but only
the three host/native-arch combinations above are actually run in CI, not
every cross-build target.

## Target image OS/architecture (what a built OCI image runs as)

| OS | Architecture | Status | Evidence |
| --- | --- | --- | --- |
| Linux | amd64 | ✅ tested | `ci-multiarch.yml` (asserts both platforms present in the index), `ci-compatibility.yml`, `ci-microvm.yml` (`boot-under-kvm`) |
| Linux | arm64 | ✅ tested | `ci-multiarch.yml`, `ci-microvm.yml` (`boot-under-hvf`, cross-built kernel) |
| Windows / macOS (as an image target) | any | ❌ not supported | the stable builder's scope is Linux `amd64`/`arm64` only (see README "Supported production scope"); there is no Windows- or Darwin-target image path |

## Container runtime (running a built image as a plain container)

| Runtime | Status | Evidence |
| --- | --- | --- |
| Docker | ✅ tested | `ci-launch.yml` matrix (`runtime: [docker, podman]`), `ci-compatibility.yml` |
| Podman | ✅ tested | `ci-launch.yml` matrix; `ci-compatibility.yml` asserts Podman's OCI runtime is `runc` |
| containerd (direct, via `ctr`) | ✅ tested | `ci-compatibility.yml`: `ctr images import`/`list`/`check` against a built image |
| Skopeo | ✅ tested, interop-only | `ci-compatibility.yml` uses Skopeo to convert the native OCI layout to a Docker archive as a cross-check; the product's own `platform-factory run`/`import` never shells out to Skopeo (see the Threat Model's T28 and the `grep -rniI skopeo` gate in `ci-security.yml`) |

## Kubernetes

| Component | Version | Status | Evidence |
| --- | --- | --- | --- |
| kind | v0.32.0 | ✅ tested | `ci-kind-multinode.yml` |
| Node image | `kindest/node:v1.36.1` (Kubernetes 1.36) | ✅ tested | `ci-kind-multinode.yml`: one control-plane + two worker nodes, RuntimeClass, distributed cancellation, worker-loss recovery |
| Other Kubernetes minor versions | — | ❌ not tested | only the one pinned `kindest/node` tag above is exercised; no version-skew matrix exists yet |

## containerd shim / RuntimeClass

| Component | Status | Evidence |
| --- | --- | --- |
| `plugins/containerd` shim (`io.containerd.platform-factory.v1`, sandboxer `shim`) | ✅ tested | `ci-compatibility.yml` builds `platform-factory-containerd` and asserts its generated `containerd-platform-factory.toml`/`RuntimeClass` YAML; `ci-kind-multinode.yml` exercises it inside a real cluster |

## Hypervisor backend (native `--isolation microvm`)

| Backend | Host OS | Guest architecture | Status | Evidence |
| --- | --- | --- | --- | --- |
| KVM | Linux | amd64 | ✅ tested against real hardware | `ci-microvm.yml` `boot-under-kvm`: confirms `/dev/kvm`, boots a real kernel, exercised through Podman, Docker, and containerd (`test-podman-kvm.sh`, `test-docker-kvm.sh`, `test-containerd-kvm.sh`) |
| KVM | Linux | arm64 | ❌ not CI-tested | `internal/hypervisor/kvm` has amd64-specific boot files (`kvm_linux_boot_amd64.go`); no arm64 KVM job exists |
| HVF (Virtualization.framework / Hypervisor.framework) | macOS (macos-15) | arm64 | ✅ tested against real hardware | `ci-microvm.yml` `boot-under-hvf`: cross-built arm64 kernel, `test-hvf-local.sh` |
| Rosetta 2 for Linux inside HVF | macOS (macos-15, Apple silicon) | Linux amd64 userspace in an arm64 guest | ✅ dedicated native proof | CI attaches Apple's read-only `VZLinuxRosettaDirectoryShare`, mounts its `rosetta` VirtioFS tag in the guest, and requires a real Linux/amd64 probe to print `PLATFORM_FACTORY_ROSETTA_LINUX_AMD64_OK`. Platform Factory probes availability but never installs Rosetta. |
| Native guest port forwarding | Linux/KVM and macOS/HVF | TCP and UDP | ✅ tested | Bounded host relays; `TestNativeHVFRealTCPAndUDP` proves both protocols through a real arm64 Linux guest, and `ci-microvm.yml` requires the equivalent real KVM path. |
| WHPX (Windows Hypervisor Platform) | Windows | amd64 | ❌ not CI-tested | `internal/hypervisor/whpx` exists as code (non-blocking per the implementation roadmap) but no CI job boots a guest under it |

## Guest kernel

| Component | Version | Evidence |
| --- | --- | --- |
| Linux guest kernel (KVM and HVF) | 6.12.98, built from `scripts/microvm/build-kernel.sh` | `scripts/microvm/build-kernel.sh:12` (`KERNEL_VERSION`), cached per-architecture in `ci-microvm.yml` |

Rosetta is a cross-architecture userspace test path, not a replacement for a
Linux host. Pure Linux executables and application behavior can run as amd64
inside the arm64 HVF guest. Tests of `/dev/kvm`, Linux host namespaces,
cgroups, seccomp, pidfd, TAP devices, AppArmor, Podman/Docker/containerd host
integration, or Linux-specific syscalls acting on the host remain on Linux CI.

## Signing / registry interoperability

This project's own publish path uses a native Ed25519/DSSE signer and a
native OCI Distribution client (see the Threat Model's "What changed since
v1.0"); the rows below are external-tool interoperability checks, not the
product's own trust path.

| Tool | Status | Evidence |
| --- | --- | --- |
| Cosign / Fulcio / Rekor (keyless GHCR signing) | ✅ tested, CI-owned, not the product path | `ci-release.yml`, `ci-microvm.yml`, `ci-benchmark.yml`, `ci-supply-chain-e2e.yml` per ADR-006/ADR-007 |
| GHCR (as a concrete OCI Distribution registry) | ✅ tested | `ci-supply-chain-e2e.yml` |
| OCI Distribution 2.8.3 through native `pf publish` + Podman pull | ✅ tested locally; CI path configured | `ci-compatibility.yml`; provider status and acceptance contract in `docs/registry-providers.md` |

## Review trigger

Re-verify this file (grep the referenced workflow files for their current
matrix/version pins, don't assume they're unchanged) whenever a
`.github/workflows/*.yml` matrix, runner image tag, kernel version, or
`kindest/node` tag changes, or at least alongside the Threat Model's own
review cadence.

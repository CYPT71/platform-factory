# Architecture decisions & scope

This document records the concrete architecture decisions evaluated for the
post-stabilization roadmap so only measured, justified evolutions receive a
contract and acceptance recipe.

## 1. Separate scheduler service vs. embedded control-plane scheduler

- **Decision:** Keep scheduling embedded in `platform-factory-control-plane`
  (`internal/control` + `internal/quota.FairScheduler` + durable snapshot
  persistence). Do **not** split out a separate scheduler binary or RPC hop.
- **Measured basis:** `internal/control` and `cmd/platform-factory-control-plane`
  handle lease submission, tenant fairness, capability/cache-locality matching,
  and durable state persistence in microseconds per operation under race-tested
  mTLS load (`multitenant_test.go`). Splitting the scheduler would add network
  failure modes and state-synchronization complexity without solving an observed
  bottleneck.
- **Revisit trigger:** Measured control-plane scheduling p99 latency > 50 ms at
  sustained > 500 concurrent workers, or an operational requirement for active-
  active multi-region HA scheduling.

## 2. Additional virtio devices beyond console, blk, and net

- **Decision:** Keep the supported MicroVM device set strictly minimal:
  `virtio-console` (serial/guest-agent transport), `virtio-blk` (rootfs/disks),
  and `virtio-net` (network). Do **not** add `virtio-fs`, `virtio-gpu`,
  `virtio-balloon`, or `virtio-vsock` without a concrete workload contract.
- **Measured basis:** Every container/MicroVM lifecycle acceptance flow
  (Podman KVM, containerd shim, HVF) is satisfied by `virtio-blk` + `virtio-net`
  + serial guest transport; adding more MMIO/PCI devices increases guest kernel
  attack surface and maintenance burden across both KVM and HVF backends.
- **Revisit trigger:** A concrete workload requiring host-shared mutable mounts
  (`virtio-fs`) or memory overcommit (`virtio-balloon`) with an explicit threat
  model and native KVM + HVF acceptance recipes.

## 3. Windows / WHPX hypervisor scope

- **Decision:** Windows WHPX remains **out of scope for MicroVM execution**
  (availability probe only). Supported native hypervisor execution targets are:
  - **Linux/amd64 KVM** (native recipe: `tests/microvm/test-podman-kvm.sh`)
  - **macOS/arm64 HVF** (native Apple Virtualization.framework via CGO +
    `scripts/microvm/hvf.entitlements`, tested in `internal/hypervisor/hvf`)
- **Contract:** Cross-compiling for Windows never implies WHPX execution support;
  a WHPX backend will only be promoted once a dedicated Windows/WHPX maintainer
  and native CI runner own boot, stop, network, persistence, and disk recovery
  acceptance tests.

## 4. Independent security review boundaries

- Internal automated tests (unit, fuzz, race, hostile-plugin, and mTLS isolation
  tests) verify regressions continuously but are **not** equated to an external
  audit.
- Scope queued for independent third-party review before claiming external
  certification:
  1. OCI rootfs extraction & symlink/whiteout handling (`internal/rootfs`, `oci/`)
  2. Linux plugin/worker sandboxing & per-destination network policy (`internal/plugin`)
  3. Cryptographic signing, DSSE verification, and mTLS tenant binding
     (`internal/signing`, `internal/attestation`, `internal/mtls`,
     `cmd/platform-factory-control-plane`)

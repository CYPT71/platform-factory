# Known limitations

## Troubleshooting

- **"binary is not executable"**: `chmod 0755 service`, then rebuild with
  `CGO_ENABLED=0`.
- **Unsupported architecture**: pass `-arch amd64` or `-arch arm64` and
  build the binary for the same target.
- **Output already exists**: select a new output directory; it is
  intentionally never overwritten.
- **Registry publication fails**: use the workflow artifact; confirm
  repository Actions permissions allow `packages:write` before retrying.

## What this project does not do

This corrects an older claim (until 2026-08-04, the README said this
project was "not a registry client, image signer, SBOM generator" — true
in v1, no longer true since v3's native supply-chain path shipped).
Current, verified limitations instead:

- **`verify-release` doesn't fetch a published image; it verifies local
  artifacts.** `internal/registry.Client` gained `GetManifest`/`GetBlob`
  on 2026-08-04, but nothing in the shipped CLI calls them yet — there is
  no `platform-factory pull`. See the [maturity matrix](reference/maturity.md).
- **Does not prove an executable is static.** Use the static build
  command in the README, or `-extra-file` for a binary that genuinely
  can't be statically linked.
- **Not a Dockerfile interpreter.** There is no Dockerfile parsing or
  build-instruction execution; inputs are an already-built executable
  plus explicit extra files.
- **The native VMM host process's self-sandboxing is partial.** KVM
  guest isolation is real and unaffected. As of 2026-08-05,
  `internal/hypervisor/sandbox` is wired into `cmd/platform-factory-runtime`'s
  startup path: `no_new_privs` and a strict classic-BPF seccomp filter apply,
  while PID/IPC/UTS namespace isolation, cgroup limits, and current plus
  bounding capability-set dropping apply whenever the host has the privilege,
  probe-gated so a host without it still launches guests rather than
  failing closed. Mount and network namespace isolation remain unimplemented
  because the runtime still opens host rootfs/TAP resources after confinement.
  See the
  [Threat Model](https://github.com/CYPT71/platform-factory/wiki/Threat-Model-and-Residual-Risks)'s
  T22 and the [maturity matrix](reference/maturity.md).
- **MicroVM on Linux/arm64 and Windows (WHPX) are untested.** Only
  Linux/amd64 (KVM) and macOS/arm64 (HVF) boot a real guest in CI. See
  the [compatibility matrix](reference/compatibility.md).
- **Windows and macOS are not supported *image targets*.** They are
  supported *hosts* for running the `platform-factory` CLI itself
  (`ci-reproducibility.yml` tests all three), but the stable builder only
  produces Linux `amd64`/`arm64` images.
- **Per-tenant quota/priority/fairness is opt-in.** A control-plane
  deployment that doesn't explicitly enable `internal/quota` gets none of
  its enforcement.
- Will not make you coffee.

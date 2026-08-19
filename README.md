# Platform Factory

[![Quality](https://github.com/CYPT71/platform-factory/actions/workflows/ci-quality.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-quality.yml?query=branch%3Amain)
[![OCI validation](https://github.com/CYPT71/platform-factory/actions/workflows/ci-oci-validation.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-oci-validation.yml?query=branch%3Amain)
[![Security analysis](https://github.com/CYPT71/platform-factory/actions/workflows/ci-security.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-security.yml?query=branch%3Amain)
[![Reproducibility](https://github.com/CYPT71/platform-factory/actions/workflows/ci-reproducibility.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-reproducibility.yml?query=branch%3Amain)
[![Runtime integration](https://github.com/CYPT71/platform-factory/actions/workflows/ci-runtime.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-runtime.yml?query=branch%3Amain)
[![MicroVM boot](https://github.com/CYPT71/platform-factory/actions/workflows/ci-microvm.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-microvm.yml?query=branch%3Amain)
[![CodeQL analysis](https://github.com/CYPT71/platform-factory/actions/workflows/ci-codeql.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-codeql.yml?query=branch%3Amain)
[![Fuzz validation](https://github.com/CYPT71/platform-factory/actions/workflows/ci-fuzz.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-fuzz.yml?query=branch%3Amain)
[![Release evidence](https://github.com/CYPT71/platform-factory/actions/workflows/ci-release.yml/badge.svg)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-release.yml)
[![Benchmark](https://github.com/CYPT71/platform-factory/actions/workflows/ci-benchmark.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-benchmark.yml?query=branch%3Amain)
[![OCI compatibility](https://github.com/CYPT71/platform-factory/actions/workflows/ci-compatibility.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-compatibility.yml?query=branch%3Amain)
[![Supply-chain E2E](https://github.com/CYPT71/platform-factory/actions/workflows/ci-supply-chain-e2e.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-supply-chain-e2e.yml?query=branch%3Amain)
[![DAST validation](https://github.com/CYPT71/platform-factory/actions/workflows/ci-dast.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-dast.yml?query=branch%3Amain)
[![Sandbox](https://github.com/CYPT71/platform-factory/actions/workflows/ci-sandbox.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-sandbox.yml?query=branch%3Amain)
[![System library scan](https://github.com/CYPT71/platform-factory/actions/workflows/ci-system-libraries.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-system-libraries.yml?query=branch%3Amain)
[![Launch matrix](https://github.com/CYPT71/platform-factory/actions/workflows/ci-launch.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-launch.yml?query=branch%3Amain)
[![Multi-arch OCI](https://github.com/CYPT71/platform-factory/actions/workflows/ci-multiarch.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-multiarch.yml?query=branch%3Amain)
[![pf init experience](https://github.com/CYPT71/platform-factory/actions/workflows/ci-pf-init-experience.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-pf-init-experience.yml?query=branch%3Amain)
[![Kind multi-node runtime](https://github.com/CYPT71/platform-factory/actions/workflows/ci-kind-multinode.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-kind-multinode.yml?query=branch%3Amain)
[![MCP server image](https://github.com/CYPT71/platform-factory/actions/workflows/ci-mcp-image.yml/badge.svg?branch=main&event=push)](https://github.com/CYPT71/platform-factory/actions/workflows/ci-mcp-image.yml?query=branch%3Amain)

Platform Factory turns a compiled executable into a deterministic OCI image,
attaches native supply-chain evidence, and runs the result as a hardened
container or a KVM/HVF microVM — one Go binary, no Docker daemon and no
external SBOM/signing/scanning tools required for the core path.

**Scope**: Platform Factory is the build, supply-chain and execution layer
that plugs into your existing stack. It does not replace Kubernetes, a CI
orchestrator (GitHub Actions, GitLab CI, Jenkins), or a general-purpose
container registry — it produces the image, the evidence, and the running
workload that those systems schedule, trigger and store.

## Who it's for

- **Platform engineers** who want one static Go binary instead of wiring
  together Docker, BuildKit, cosign, syft and skopeo for a build-to-deploy
  pipeline.
- **Security-conscious teams** who need byte-for-byte reproducible builds and
  independently verifiable SBOM/provenance/signature evidence, without
  trusting a remote build service with source or credentials.
- **Teams running legacy or non-containerizable binaries** — dynamically
  linked, or requiring stronger-than-namespace isolation — who want microVM
  execution without operating Firecracker or Cloud Hypervisor.

## Capabilities

**Build** — a deterministic OCI image builder with no Docker daemon, BuildKit,
or CGO dependency; a project lifecycle (`detect`, `freeze`, `build`, `run`,
`diff`, `migrate`); and a language-neutral pipeline engine that runs each
stage in a fresh, sandboxed namespace and extends via out-of-process,
signature-verified plugins.

**Supply chain** — native SBOM generation, SLSA-style provenance, Ed25519/ECDSA
signing, and policy evaluation, all in-process; hermetic rebuild verification
proves a build is byte-identical before it's trusted.

**Distribution** — a native Registry client with resumable uploads, `publish`
with pre-flight verification, and `deploy`/`rollback` against a restricted
Kubernetes Deployment contract.

**Execution** — the same `run` verb launches a hardened container (Docker or
Podman) or a native KVM/HVF microVM; containerd and a Kubernetes
`RuntimeClass` can select the microVM path per workload without changing the
node's default runtime.

## What makes this different

Every stage validates its own output rather than trusting the one before it —
the OCI layout, SBOM, provenance and signature all come from the same native
Go implementation instead of five external tools with five different trust
boundaries. The tradeoff is a stricter build contract than a general
Dockerfile: inputs must be locked and pre-built, and automatic dependency
resolution stays out of scope on purpose, in exchange for output whose
determinism and evidence chain the tool itself can verify end to end.

## Quick start

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o service ./cmd/your-service
go run ./cmd/oci-builder -binary ./service -output ./oci-image -arch amd64
go run ./cmd/platform-factory verify ./oci-image
go run ./cmd/platform-factory run --runtime podman ./oci-image
```

Install the full CLI (`platform-factory`, alias `pf`) locally:

```bash
./install.sh
pf version
```

## Documentation

- [CLI guide](docs/cli-guide.md) — full command reference, installation,
  testing, CI/CD, Dockerfile consumer, MicroVM support, debugging.
- [Architecture and OCI layout](docs/architecture/oci-layout.md)
- [Security model](docs/security/model.md)
- [API compatibility policy](docs/api-compatibility.md)
- [Plugin marketplace](docs/plugin-marketplace.md) — Git/SemVer publishing,
  fuzzy search, signed installation and TUI workflows for junior, intermediate
  and senior users.
- [Compatibility matrix](docs/reference/compatibility.md) — host OS, image
  OS/architecture, container runtime, Kubernetes and hypervisor backend
  support, each backed by the CI workflow that tests it.
- [Maturity matrix](docs/reference/maturity.md) — stable vs. beta/alpha/experimental.
- [containerd/Kubernetes runtime guide](docs/containerd-kubernetes.md)
- [Troubleshooting and limitations](docs/limitations.md)
- [Examples](examples/README.md) — runnable, indexed by capability.

Production documentation lives in the [project wiki](https://github.com/CYPT71/platform-factory/wiki): start with the [production adoption guide](https://github.com/CYPT71/platform-factory/wiki/Production-Adoption-Guide), then review the [architecture decisions](https://github.com/CYPT71/platform-factory/wiki/Architecture-Decision-Records), [threat model](https://github.com/CYPT71/platform-factory/wiki/Threat-Model-and-Residual-Risks), [GHCR/Cosign/Kubernetes demonstration](https://github.com/CYPT71/platform-factory/wiki/GHCR-Cosign-Kubernetes-E2E), [benchmarks](https://github.com/CYPT71/platform-factory/wiki/Benchmarks), and [OCI compatibility matrix](https://github.com/CYPT71/platform-factory/wiki/OCI-Compatibility).

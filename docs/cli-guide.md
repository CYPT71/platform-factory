# CLI Guide

Detailed usage for the `platform-factory` CLI (alias `pf`) and the lower-level
`oci-builder` tool it's built on. See the [README](../README.md) for a
higher-level orientation.

## Supported production scope

The stable builder supports deterministic, single-process Linux OCI images for
`amd64` and `arm64`. Statically linked ELF executables are the preferred input.
Dynamically linked glibc or musl ELF executables are supported only when the
dynamic linker and the complete transitive `DT_NEEDED` closure are supplied as
`-extra-file` inputs; the builder rejects missing libraries and architecture
mismatches. The runtime is non-root by default (`65532:65532`) and supports
read-only root filesystems through explicitly declared writable paths or
volumes.

Python, Node.js, Java and .NET use production runtime-first profiles: supply a
pinned runtime executable as `-binary`, its complete native ELF closure as
`-extra-file`, and locked application files as additional explicit inputs.
The builder never downloads packages or executes a package manager. Automatic
framework package installation, multi-process supervision, cross-architecture
dependency discovery and implicit host imports remain experimental.

Runtime metadata can be declared in a strict JSON contract:

```bash
go run ./cmd/oci-builder \
  -binary ./service \
  -output ./oci-image \
  -config ./examples/platform-factory.json \
  -arch amd64
```

Unknown configuration fields, unsafe paths, invalid users, ports and
healthchecks are rejected. See [`examples/platform-factory.json`](../examples/platform-factory.json)
for all supported fields.

The `profile` field is enforced: `static` rejects dynamically linked ELF
inputs, while `glibc` and `musl` require the matching interpreter and a
complete dependency closure. `user` accepts only a positive numeric UID or
`UID:GID`. When `identity_files` is enabled, deterministic `/etc/passwd`,
`/etc/group` and `/etc/nsswitch.conf` files are generated, and `HOME` points to
the declared non-root home directory.

`system_files` can package an explicitly supplied CA bundle, timezone file and
locale archive at their conventional runtime paths. Sources must be regular
files and are copied read-only; the builder never imports host state
implicitly. Empty values keep the scratch image minimal.

The stable execution model is one foreground process. It receives `SIGTERM`
and `SIGINT` directly as container PID 1 and is responsible for graceful
shutdown. Exit codes are preserved by the runtime. Applications that fork
children should enable the runtime's minimal init (`docker run --init`, or the
equivalent Kubernetes/runtime setting) so reparented processes are reaped.
Bundling a process supervisor or running multiple long-lived services in one
image remains experimental.

### v1 scope and limits

The v1 milestone covers the deterministic OCI builder, the project
lifecycle (`detect`, `plan`, `freeze`, `build`, `run`, `diff`,
`migrate`) and the unified `platform-factory launch` flow, on Linux
`amd64`/`arm64` with Docker or Podman as the container runtime. Two
clean builds of the same project produce the same digest;
`platform-factory diff` explains any divergence, and the launch-matrix CI
workflow proves the flow end to end against both runtimes.

Formats that remain experimental in v1 and may change between releases:
the project config schema (version 1; `platform-factory project migrate`
rewrites older documents and future bumps ship with a migration), the
`platform-factory.dev/project-plan/v1` plan JSON, the freeze inventory
`.platform-factory/freeze.lock.json`, and the structured JSONL build events.
Semantic layers are opt-in (`--semantic-layers` or `semantic_layers:`)
because enabling them changes layer digests. Go and Rust projects map
to the `static` profile on purpose: their runtime behavior is derived
from the built ELF executable, not the language name. `publish` now uses the
project-owned Registry, SBOM, provenance, policy and Ed25519 engines. Runtime
and cluster operations still delegate to the explicitly selected
Docker/Podman or Kubernetes client.

### v2 pipeline, plugins and sandbox

The v2 milestone adds a language-neutral pipeline engine reachable from
the shipped CLI. `platform-factory pipeline plan PIPELINE.json` validates a
`platform-factory.dev/v1alpha1` document and prints its stages, dependency
levels, canonical fingerprint and required-vs-available capabilities;
`platform-factory pipeline run` executes the DAG with a continuous ready-set
scheduler (independent branches overlap), materializes declared
artifacts between stages with consumption-time digest verification, and
writes a `platform-factory.dev/journal/v1` result journal.

On Linux the stage executor runs each stage in fresh user, mount, PID,
network, IPC and UTS namespaces: writes are confined to the stage root,
`network: none` is enforced (only loopback exists), mounts honor
`read_only`, `read_only_root` and `non_root` are applied, and CPU and
PID limits use per-stage cgroup v2 children. Everything the host cannot
provide fails closed with an actionable message. Secrets are delivered
on a per-stage in-memory tmpfs that vanishes with the namespace, are
redacted from captured output, and never enter cache keys, stage roots
or layouts. `--sandbox require` refuses to run unsandboxed; `--sandbox
off` uses the minimal executor, which refuses stages declaring secrets
or sandbox policies rather than run them unconfined.

Plugins extend the engine out of process. A plugin ships a signed
`plugin.json` manifest that pins its executable by sha256 digest; the
host verifies the digest (always) and an Ed25519 signature (unless
`--allow-unverified-plugin`, which still enforces the digest), then
consults the plugin for `detect`, `freeze` and `plan`. Go extensions implement
only `sdk/plugin.LanguageExtension`; its reference runtime owns the protocol.
Python, JavaScript/TypeScript and C# implementations speak the same stable v1
framing without depending on platform-factory internals. The
`platform-factory-conformance` binary applies the same capability and
transport suite to every language without requiring this repository at
runtime.

Use `platform-factory-conformance publication` for the public OCI Registry and
Kubernetes target contract: valid and hostile references, immutable image
digests, Kubernetes identity validation, and byte-stable hardened Service/Job
manifests. It runs offline from the standalone binary.

Network policy `full` is implemented; `resolve` uses a project-owned,
explicit-upstream DNS forwarder over an inherited namespace-safe relay. The v3 publication path uses the
native Registry, SBOM, provenance, signing and policy engines; it verifies the
layout and installs evidence before moving a mutable tag.

### Hermetic rebuild verification (v3, in progress)

`platform-factory build --rebuild=N --require-identical EXECUTABLE` builds the
target N times into fresh directories that cannot reuse each other's
output, then compares every layout with the same engine `platform-factory
diff` uses. When the rebuilds are byte-identical the layout is installed
and the result reports `reproducible: true`; otherwise it emits a
structured report of the differing descriptors and, under
`--require-identical`, exits non-zero without installing anything. The same v3
path now includes chunked CAS storage, persistent Registry upload sessions,
native supply-chain evidence and a fail-closed publication policy.

### Automatic detection

Runnable and configuration-backed examples for every major capability are
indexed in [`examples/`](../examples/README.md): OCI construction, project
freeze/launch, pipelines and CAS reuse, plugins, native supply-chain evidence,
KVM/HVF MicroVMs, containerd/Kubernetes, distributed scheduling and structured
observability.

`platform-factory detect PATH` inspects inputs without executing them and
emits JSON. It recognizes static and dynamic ELF files, `amd64`/`arm64`,
glibc/musl interpreters, native dependencies, shebang scripts, Python and
Node lockfiles, JAR archives and .NET assemblies. A directory matching more
than one ecosystem is reported as ambiguous and returns exit code 2 unless
the caller explicitly passes `--accept-ambiguous`.

```bash
go run ./cmd/platform-factory detect ./my-app
```

`platform-factory inspect LAYOUT` returns the verified platforms, manifest
digests and blob counts as JSON. `platform-factory verify LAYOUT` applies the
same strict native Go verification as a release gate, including recursive
descriptor digests and sizes, config/platform consistency, layer `diff_id`,
duplicate or traversing paths, links, devices and FIFOs. Both commands
support single-platform and deterministic multi-platform layouts.

`platform-factory sbom PATH [PATH...]` generates a software bill of materials
for the given files and directories entirely in native Go — no `syft` or
other external tool. Each component records the file's sha256 digest, size,
detected kind and native ELF dependencies; directories are walked
recursively and only regular files become components. The output is sorted
by component name, so it is byte-for-byte reproducible, and `--format text`
prints a tab-separated summary instead of JSON.

```bash
platform-factory sbom ./service ./assets > sbom.json
```

`platform-factory` is the recommended single interface. Running it without
arguments prints a compact command map; `platform-factory COMMAND --help`
shows command options, and `platform-factory version` prints the build
version. Short top-level commands (`build`, `compose`, `run`, `microvm`)
remain supported alongside the more descriptive `image`, `container`, and
`vm` forms. `cmd/oci-builder` remains available for backward-compatible
low-level automation, but no longer has build capabilities missing from
`platform-factory`.

The complete local-to-production workflow is available from the same binary:

```bash
# Build and compose both platform manifests atomically.
platform-factory build -o ./service-multi \
  --image example/service --tag v1 \
  --platform linux/amd64=./service-amd64 \
  --platform linux/arm64=./service-arm64

# Preview, then publish with native SBOM, signature and provenance engines.
platform-factory publish --dry-run --sign --sbom \
  --provenance provenance.json --policy policy.json --evidence evidence.json \
  ./service-multi ghcr.io/example/service:v1
platform-factory publish --yes --sign --sbom \
  --provenance provenance.json --policy policy.json --evidence evidence.json \
  ./service-multi ghcr.io/example/service:v1

# Deploy only an immutable digest, then perform an explicitly confirmed rollback.
IMAGE_REF=$(platform-factory publish --yes --sign --sbom --format reference \
  --provenance provenance.json --policy policy.json --evidence evidence.json \
  ./service-multi ghcr.io/example/service:v1)
platform-factory deploy --dry-run --name service \
  "$IMAGE_REF"
platform-factory deploy --name service \
  "$IMAGE_REF"
platform-factory rollback --yes service

# Use the same verb for a hardened container or a microVM.
platform-factory run --isolation=container --runtime=podman \
  -p 127.0.0.1:8080:8080 \
  -p 127.0.0.1:8443:8443 \
  ghcr.io/example/service@sha256:<digest>
platform-factory run --isolation=microvm --layout=./service-multi \
  -p 127.0.0.1:8080:8080/tcp \
  --port 127.0.0.1:5353:53/udp
```

`publish` verifies the source layout before any Registry request and requires
`--source-ref` when a catalog contains multiple image names. Blob uploads
resume across process crashes from persistent, registry-reconciled sessions.
Registry mutation requires `--yes`; `--dry-run` prints every operation.
`deploy` emits or applies a
restricted Kubernetes Deployment (non-root, RuntimeDefault seccomp, read-only
root filesystem, no privilege escalation, all capabilities dropped), requires
a digest-pinned image, and waits for rollout completion. `rollback` also
supports `--dry-run`, `--to-revision`, and rollout waiting.

With a project configuration whose `image` includes a registry,
`platform-factory launch --publish --yes` freezes dependencies, requires two
digest-identical builds, creates native production evidence, publishes, then
runs the configured workload. `--publish=HOST:CONTAINER` remains the
port-forwarding syntax and is never interpreted as a registry request.

Build and compose results remain JSON by default for backward-compatible
automation. Add `--format=text` for a concise terminal summary.

Runtime port forwarding accepts repeatable `-p`, `--port`, and `--publish`
aliases:

```bash
platform-factory run --network bridge \
  -p 8080:80 -p 8443:443 --port 5353:53/udp \
  "$IMAGE_REF"
```

Each value accepts `PORT`, `HOST:GUEST`, or `IP:HOST:GUEST`, optionally
followed by `/tcp` or `/udp`. The same syntax works with
`--isolation=microvm`.

A verified local OCI layout can be run directly. If its image is absent from
Docker or Podman, the CLI streams it into the selected runtime automatically;
no manual `tar` or `runtime load` step and no temporary archive are required:

```bash
platform-factory run --runtime podman --network bridge \
  -p 127.0.0.1:8080:8080 \
  ./oci-image
```

For a layout containing several image names, select the desired reference:

```bash
platform-factory run --runtime podman --layout ./catalog \
  --network bridge -p 8080:8080 example/service:v1
```

The layout is strictly verified before import. Existing runtime images are
reused, and the imported reference is checked again before execution.

An OCI Image Layout distributed as a gzip-compressed tar archive can be
verified without trusting an external extractor:

```bash
pf verify --archive-format oci-layout.tar.gz ./image.oci.tar.gz
pf inspect --archive-format oci-layout.tar.gz ./image.oci.tar.gz
# Pin downloaded bytes before parsing them:
pf verify --archive-format oci-layout.tar.gz --sha256 sha256:HEX ./image.oci.tar.gz
```

PF bounds compressed expansion, file count and total payload, rejects links,
duplicate/traversing paths, secret markers and trailing gzip members, extracts
into a private temporary directory, then runs the same digest/descriptor
verifier used for a directory layout. The temporary tree is removed on both
success and failure. `--sha256` accepts exactly one lowercase SHA-256 and fails
closed on a byte mismatch. Plain tar, generic source archives and Docker Save are
different formats and are not accepted by this option.

Projects may compose multiple local trees/files with `include` and
`shared_deps`. Their destinations must be unique clean absolute image paths.
Any project using these additional sources must run `pf freeze`; `pf build`
recomputes size and SHA-256 for every frozen file and refuses missing, added or
changed inputs before invoking the build command or writing an OCI layout.
Fresh projects also persist `.pf/build.pipeline.json`. `pf build` strictly
decodes and validates this DAG—including cycle detection—before freeze or any
custom command. Existing projects without the file remain supported by
deriving and validating the equivalent DAG from `pf.yaml` in memory.

Docker Save archives have their own explicit verifier:

```bash
pf verify --archive-format docker-save.tar ./image.docker.tar
```

PF parses `manifest.json` strictly, bounds entries and bytes, accepts regular
files only, validates every referenced config/layer, and checks SHA-256 when a
Docker archive filename is content-addressed. This performs no `docker load`
and therefore cannot mutate a local daemon.

Inspect an immutable OCI Registry manifest directly by digest:

```bash
pf registry inspect registry.example/team/service@sha256:HEX
```

Mutable tags are refused. The native Distribution client bounds the response,
does not follow any HTTP redirect (upload `Location` values are separately
validated and must preserve the registry host), and recomputes SHA-256 from the response
body before returning its media type and size. Authentication uses
`PLATFORM_FACTORY_REGISTRY_USERNAME` and
`PLATFORM_FACTORY_REGISTRY_PASSWORD` or the matching flags.

For an initialized application, the beginner path is shorter:

```bash
pf init --yes --engine docker .   # or: --engine podman
pf launch
```

A local source tarball is initialized into a new directory explicitly:

```bash
pf init --archive-format tar.gz --extract-to ./service ./service.tar.gz
```

`tar` and `tar.gz` are extracted by PF itself with bounded file count,
decompressed bytes and payload size. Absolute/traversing/duplicate names,
links and special files are rejected. The destination must not already exist;
it is removed on extraction or initialization failure. With `--dry-run`, PF
uses a private temporary extraction and leaves `--extract-to` absent.

`pf launch` freezes dependencies when required, builds and verifies
`.platform-factory/image`, imports it into the selected local engine only when
missing, then launches it with the configured security defaults. PF never
starts Docker Desktop or a Podman machine implicitly.

Install shell completion with:

```bash
source <(platform-factory completion bash)
# zsh: platform-factory completion zsh > "${fpath[1]}/_platform-factory"
# fish: platform-factory completion fish | source
```

## Local environment and installation

Build every command into a disposable, project-local environment:

```bash
# Linux or macOS
scripts/local/bootstrap.sh
source .platform-factory-env/activate
platform-factory version

# Windows PowerShell
.\scripts\local\bootstrap.ps1
. .\.platform-factory-env\Activate.ps1
platform-factory version
```

The environment contains the CLI, builder, MicroVM/distributed commands and
all eight official language plugins, plus activation files for POSIX shells,
PowerShell, and CMD. It is an isolated binary environment—not a Python
virtualenv—and does not modify the system by default.

Cross-build a distributable environment or install into an explicit prefix:

```bash
scripts/local/bootstrap.sh --target linux/amd64 --env dist/linux-amd64
scripts/local/bootstrap.sh --target windows/amd64 --env dist/windows-amd64
scripts/local/bootstrap.sh --install "$HOME/.local"

# PowerShell equivalents
.\scripts\local\bootstrap.ps1 -TargetOS windows -TargetArch amd64 `
  -Environment .\dist\windows-amd64
.\scripts\local\bootstrap.ps1 -InstallPrefix "$HOME\.local"
```

Existing environments are never replaced implicitly; pass `--clean` or
`-Clean`. Installing copies the compiled commands and official plugins into
`PREFIX/bin`, so administrator privileges are needed only when the chosen
prefix itself requires them. Selected runtime integrations such as
Docker/Podman, QEMU, `kubectl`, and `virtctl` remain explicit external
dependencies and are not silently installed; building, verifying and
publishing OCI content does not install Skopeo, Cosign or Syft.

### Release packages

Every semantic release attaches relocatable native distributions:

- `platform-factory-VERSION-linux-amd64.tar.gz`;
- `platform-factory-VERSION-darwin-arm64.tar.gz`;
- `platform-factory-VERSION-windows-amd64.zip`.

Each includes `pf`, `platform-factory`, the companion commands, all official
language plugins, activation scripts, `INSTALL.txt`, and a versioned
`MANIFEST.json` containing the size and SHA-256 of every payload. A sibling
`.sha256` file authenticates the archive. Release CI builds on the matching
native runner, extracts the package and runs `pf version` before publishing
it. Maintainers can reproduce all three locally with
`scripts/local/package-release.sh OUTPUT_DIR VERSION`; output paths are never
overwritten.

### Interactive installer

`bootstrap.sh` always builds the full command set for CI and cross-builds.
End users who only want specific components can instead run one of two
interactive installers, either of which builds and installs just the
binaries selected — the base CLI (`platform-factory`, with its `pf` alias
next to it — see below) is always included, and `oci-builder`, MicroVM
support (`microvm-init`, `microvm-initramfs`, `platform-factory-runtime`),
and the distributed platform (`platform-factory-control-plane`,
`platform-factory-worker`) are opt-in:

```bash
# Quickest: root-level entry point, delegates to scripts/local/install.sh
./install.sh

# Plain bash + ANSI, no build dependencies beyond Go
scripts/local/install.sh

# Go TUI (bubbletea/huh)
go run ./cmd/platform-factory-installer
```

Both fall back to a plain sequential build outside a terminal, so they can
also be scripted:

```bash
scripts/local/install.sh --components builder,microvm --prefix "$HOME/.local/bin" --yes
go run ./cmd/platform-factory-installer -components builder,microvm -prefix "$HOME/.local/bin" -yes
```

Pass `--list`/`-list` to either one to see the available components.

`pf`, installed as a symlink alongside `platform-factory`, is a plain alias
for it: `pf <anything>` and `platform-factory <anything>` run the identical
binary. There is no separate delegator process and no separate `platform-factory`
binary to keep in sync — `pf` is just a shorter name for the same command
surface documented throughout this guide.

Compose independently built layouts into one deterministic multi-manifest OCI
layout:

```bash
platform-factory compose --output ./catalog \
  ./api-amd64 ./api-arm64 ./worker-amd64
platform-factory verify ./catalog
```

`compose` preserves each manifest's
`org.opencontainers.image.ref.name` annotation, so one index may contain an
`amd64`/`arm64` pair for the same tag, multiple tags pointing to shared blobs,
and unrelated applications. Inputs are strictly verified first, blobs are
deduplicated by digest, and the new layout is atomically installed only after
the combined result passes verification. `oci-builder compose -output
./catalog ...` exposes the same operation.

`run` delegates only to Docker or Podman and applies read-only rootfs,
no-new-privileges, dropped capabilities, an init, no network, and bounded
CPU, memory and PID defaults. Use `--network=bridge` only when connectivity
is explicitly required.

`build` accepts compiled application executables or the pinned runtime binary
for the `python`, `node`, `java` and `dotnet` profiles. Directly passing a
detected source directory remains rejected: consumers must provide locked,
prebuilt application files explicitly. Production profile examples live in
[`examples`](../examples/).

The unified build command exposes the complete builder contract:
`--platform` (or `--arch`/`--os`), `--image`, `--tag`, `--entrypoint`,
`--profile`, reproducible `--created`, repeatable `--label` and
`--extra-file`, compression selection, and the strict runtime `--config`.

CI enforces a fail-closed Go module license policy and publishes deterministic
JSON inventories for module licenses and `govulncheck` results. Native system
libraries supplied through `extra-file` or `system_files` remain the
consumer's responsibility: their package provenance, vulnerability status
and redistribution license must be assessed before production use.

## Build and use (low-level `oci-builder`)

Build the application statically, then create a layout:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o service ./cmd/your-service
go run ./cmd/oci-builder -binary ./service -output ./oci-image -arch amd64 \
  -label security.tls.minimum=1.2 -label security.mtls=required
```

Supported architectures are `amd64` and `arm64`; the operating system is `linux`. The CLI exits with `2` for malformed arguments and `1` for build failures. Run `go run ./cmd/oci-builder -h` for the complete flag reference.

CI builds each architecture with a native host-side builder, validates the
target ELF and single-platform layout independently, then assembles a
deterministic multi-platform OCI index. Reproducibility is checked
byte-for-byte separately for `linux/amd64` and `linux/arm64`; successful
cross-compilation does not claim that an ARM64 payload was executed on an
AMD64 runner.

For local multi-platform or multi-application composition, build each target
independently and merge the resulting layouts:

```bash
go run ./cmd/oci-builder -binary ./service-amd64 -output ./service-amd64-layout \
  -arch amd64 -image example/service -tag v1
go run ./cmd/oci-builder -binary ./service-arm64 -output ./service-arm64-layout \
  -arch arm64 -image example/service -tag v1
go run ./cmd/oci-builder compose -output ./service-multi \
  ./service-amd64-layout ./service-arm64-layout
go run ./cmd/platform-factory inspect ./service-multi
```

### Packaging a dynamically-linked or legacy binary

A binary doesn't have to be static. `-extra-file /container/path=host/path` (repeatable) adds any additional file to the image at a fixed path - typically the ELF interpreter and every shared library a dynamically-linked binary needs, found with `ldd`:

```bash
go run ./cmd/oci-builder -binary ./legacy -output ./oci-image -arch amd64 -entrypoint /app/legacy \
  -extra-file /lib64/ld-linux-x86-64.so.2=/lib64/ld-linux-x86-64.so.2 \
  -extra-file /lib/x86_64-linux-gnu/libc.so.6=/lib/x86_64-linux-gnu/libc.so.6
```

`scripts/local/package-dynamic-binary.sh BINARY OUTPUT [ENTRYPOINT]` automates this: it runs `ldd` on the binary, turns every real dependency into an `-extra-file`, and builds the layout. It must run on Linux, matching the binary's own architecture (`ldd` traces execution). This is what makes a genuinely legacy binary - one that can't be statically recompiled - work through this project's [Dockerfile consumer](#dockerfile-consumer) or [`scripts/microvm`](#microvm-support) exactly like any other layout.

## Reusable mTLS configuration

`internal/mtls` offers `ClientConfig` and `ServerConfig`. Both require TLS 1.2 or newer without setting a maximum version, so Go automatically enables TLS 1.3. `Options` accepts PEM CA data and `tls.Certificate` identities. A server configured with `MutualTLS: true` requires a CA bundle and verifies every client certificate.

## Testing and CI/CD

Unit tests are colocated with the package they exercise, as Go requires for
access to unexported identifiers. Tests that instead exercise the product
as an external, black-box consumer — conformance vectors, the example
catalog, and shell-driven kind/microVM integration checks — live under
[`tests/`](../tests/), grouped by what they cover (`tests/conformance`,
`tests/examples`, `tests/kind`, `tests/microvm`). `go test ./...` runs both
together.

### Threat model and reproducibility boundary

CI treats pull requests, artifacts, registry responses, and generated OCI layouts as untrusted. Validation independently checks layout digests, descriptor sizes, layer paths, and hostile mutations before a runtime test or release step consumes an artifact. Reproducible means that, on the fixed runner image with the pinned Go toolchain, the same revision and build inputs produce identical Linux `amd64` executable and OCI-layout bytes; registry manifests, SBOM/provenance attestations, signatures, and release metadata are deliberately external evidence and are not claimed to be byte-identical.

All CI jobs run on the fixed GitHub-hosted `ubuntu-24.04` image, use
read-only repository permissions unless publication requires package access,
and set an explicit job timeout. Actions are pinned by commit SHA and checked
by the workflow-policy verifier. The badges in the README show the status of
the default branch; open a badge to inspect its run logs and downloadable
evidence.

| Workflow | File | Trigger | Validation and evidence |
| --- | --- | --- | --- |
| **Quality** | `ci-quality.yml` | Push, pull request, weekly schedule | Matrix over `amd64`/`arm64`: records the Go environment, checks formatting/`go vet`/tests/race detection/85% coverage (amd64 leg), cross-compiles and ELF-verifies both architectures, then self-builds and self-verifies an OCI layout (including a reproducible-timestamp and hostile-layout-mutation re-check). |
| **OCI validation** | `ci-oci-validation.yml` | Push and pull request | Builds a deterministic layout, verifies every descriptor/blob/layer, rejects hostile mutations, and uploads validation, checksum, and filesystem evidence. |
| **Security analysis** | `ci-security.yml` | Push, pull request, weekly schedule | Two jobs: `static-analysis` validates workflow pinning/safety, runs `go vet`, pinned `govulncheck`, and race tests, and rejects risky process-execution or insecure-TLS APIs; `pr-policy` (pull requests only) rejects forbidden tracked build output, policy markers, and whitespace errors relative to the PR base. |
| **Reproducibility** | `ci-reproducibility.yml` | Push, pull request, weekly schedule | Builds the executable and OCI layout twice in isolated temporary directories, compares every byte, and uploads SHA-256 evidence. |
| **Runtime integration** | `ci-runtime.yml` | Push and pull request | Generates a static HTTP API, builds its OCI layout through the multi-stage Docker consumer, verifies `/healthz` returns `PONG` and `/` returns `HELLO WORLD`, exercises SIGTERM/SIGINT, and proves a second restricted launch with networking disabled plus CPU, memory and PID limits before checking the Kubernetes restricted-runtime manifest contract offline. |
| **MicroVM boot** | `ci-microvm.yml` | Push and pull request | Builds `cmd/example-service` into an OCI layout, boots it under KVM via `scripts/microvm/run-microvm.sh` (from-source kernel, cached), and smoke-tests `/healthz` through the guest's forwarded port. |
| **CodeQL analysis** | `ci-codeql.yml` | Push, pull request, weekly schedule | Uses GitHub's maintained CodeQL bundle to build and analyze Go, then uploads results to the repository security dashboard. |
| **Fuzz validation** | `ci-fuzz.yml` | Push to `main`, pull request, weekly schedule | Runs bounded fuzzing against label parsing and entrypoint validation boundaries. |
| **Release evidence** | `ci-release.yml` | Semantic version tag (`vMAJOR.MINOR.PATCH`) | Separates validated amd64/arm64 layout builds, protected-environment multi-platform publication, digest signing, and GitHub release creation; publishes per-platform BuildKit SBOM/provenance evidence and attaches Docker-loadable archives plus reports ZIP. |

Run all checks locally:

```bash
go test ./...
go test -race ./...
go test ./... -coverprofile=coverage.out
go tool cover -func=coverage.out
go vet ./...
```

GitHub Actions verifies formatting, vet, normal and race tests, enforces at least 85% statement coverage, builds a static sample, and checks the OCI artifacts. Before any layout is generated, CI verifies the executable is an ELF64 binary for the requested architecture and rejects binaries with a `PT_INTERP` program header (dynamic linking). Pushing a semantic version tag (`vMAJOR.MINOR.PATCH`) creates the protected release after independent amd64/arm64 layout validation, multi-platform publication, and digest-signature gates succeed. It attaches `platform-factory-image-amd64.tar`, `platform-factory-image-arm64.tar`, and `platform-factory-reports.zip`. The workflow also publishes one multi-platform index at `ghcr.io/<owner>/<repository>@sha256:<digest>` using `GITHUB_TOKEN` with `packages:write`.

### GHCR deployment

Pull the published image with an OCI-capable client or configure your runtime to use `ghcr.io/<owner>/<repository>@sha256:<digest>`. Ensure the deployment supplies the runtime hardening options described above.

### Artifact deployment

1. Open the release created for the semantic version tag and download the archive matching the target (`platform-factory-image-amd64.tar` or `platform-factory-image-arm64.tar`) plus `platform-factory-reports.zip`.
2. Verify `image-tar.sha256`, then load the matching archive, for example: `sha256sum --check image-tar.sha256 && docker load --input platform-factory-image-amd64.tar`.
3. Extract the evidence bundle: `unzip platform-factory-reports.zip -d release-reports`.
4. Inspect `release-reports/publication-link.txt`, `signature-verification.json`, `image.digest`, and `verified-layout.tar` to independently link the validated layout to the signed published image digest.

## Dockerfile consumer

`Dockerfile` demonstrates a separate, multi-stage consumer. Its first stage validates `oci-layout`, `index.json`, and blob filenames; its second stage extracts the layer and reads the layout's own OCI config to symlink a fixed `/entrypoint` to whatever container path the image actually declares (not assumed to be `/app/service` - Dockerfile syntax can't compute a static `ENTRYPOINT` from build output any other way); the final `scratch` stage copies only the extracted root filesystem, declares the numeric non-root user, and sets `ENTRYPOINT ["/entrypoint"]`. Put a downloaded/extracted layout at `./oci-image` and run `docker build -t service:local .`. The Dockerfile does not replace runtime read-only-rootfs or no-new-privileges settings.

## MicroVM support

`scripts/microvm/run-microvm.sh` boots an already-built OCI image layout
directly under plain QEMU/KVM instead of a container - the same consumer
role the `Dockerfile` plays, for running under the strongest practical
local isolation or for a legacy/unusual binary that can't be containerized
at all. A from-source kernel, a cpio initramfs, and a tiny stdlib-only Go
init; no Firecracker/Cloud Hypervisor and no new Go module dependency.
The init forwards termination signals, preserves the application exit code,
and drains exited descendants reparented to PID 1 before powering off.
The initramfs assembly is byte-reproducible, the resolved kernel configuration
is scanned for hardening gaps, and CI records kernel source/config/image
digests plus the boot bundle digest and a deterministic CycloneDX kernel SBOM.
Evidence from successful `main` runs is
signed keylessly; pull requests and non-main branches cannot mint that signing
identity. A CI-only build flag enables the otherwise unavailable
`/debug/exit` endpoint to prove guest-initiated shutdown and PID 1 poweroff.
Linux + KVM only, dynamic across amd64/arm64 hosts. See
[MicroVM Support](https://github.com/CYPT71/platform-factory/wiki/MicroVM-Support) for the KVM install procedure,
usage, and its scope/provenance/evidence model.

## Debugging minimal containers

Use OCI tooling to inspect descriptors and verify digests before deployment. For a running minimal/distroless Docker container, `nsenter` is often preferable to `docker exec` because it does not require a shell in the image:

```bash
PID=$(docker inspect -f '{{.State.Pid}}' <container>)
sudo nsenter --target "$PID" --mount --uts --ipc --net --pid
```

Within the target namespaces, inspect `/proc/1`, `/proc/1/mountinfo`, `/proc/1/environ` (subject to permission), `ip addr`, `ip route`, and the mounted root filesystem. For containerd, first obtain the task PID with `ctr -n <namespace> tasks ls`, then use the same `nsenter` command. On Kubernetes, use an authorized node-debug session (for example `kubectl debug node/<node> -it --image=busybox`) and enter the target container PID from the node runtime.

`nsenter` requires host root/CAP_SYS_ADMIN-equivalent privileges and exposes namespaces, processes, mounts, network state, and potentially secrets. Restrict it to incident responders, audited hosts, and approved maintenance windows. Prefer `docker exec` or `kubectl exec` for normal application-level diagnostics; choose `nsenter` for namespace, mount, network, or distroless-image investigation.

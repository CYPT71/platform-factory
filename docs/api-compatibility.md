# API compatibility policy

Every public contract is versioned in its import path: `api/migration/v1`,
`api/oci/v1`, `api/pipeline/v1`, and `api/plugin/v1`. Pipeline documents use
the stable `platform-factory.dev/v1` DTOs and validation from `api/pipeline/v1`.

The supported Go SDK lives under `sdk/`: `sdk/pipeline` exposes pipeline
loading and analysis, while `sdk/microvm` exposes both microVM configuration
and the backend-neutral VMM lifecycle. `sdk/plugin` provides the reference Go
runtime and the stable, language-neutral extension protocol. MicroVM and VMM
contracts live in `api/microvm/v1` and `api/vmm/v1`. New contracts must never
be added to an unversioned `api` directory.

Native implementations are deliberately not public SDK packages. KVM, HVF,
and WHPX live under `internal/hypervisor/{kvm,hvf,whpx}`; authenticated guest
channels live in `internal/guest`; boot bundles and durable machine state live
in `internal/runtime`. External consumers use the public contracts rather than
importing these implementation packages. Inside the repository, application
and domain packages use inward-facing internal contracts; command composition
roots adapt public SDKs to those contracts. `internal/archtest` rejects imports
of `api` or `sdk` from internal packages, and imports of `internal` from public
contracts and SDKs. See the [source map](architecture/oci-layout.md#source-map).

Within a stable major version:

- existing JSON fields keep their names, types, and meaning;
- new fields are optional and default to the previous behavior;
- enum values are never repurposed;
- accepted documents remain accepted with the same observable DAG, canonical
  fingerprint, and cache-key semantics;
- security fixes may reject input only when accepting it would violate a
  documented safety invariant.

`api/pipeline/v1alpha1` and `api/pipeline/v1beta1` documents remain accepted
for migration.
Their frozen JSON fixtures run through the same strict decoder and conformance
engine as `v1`. Deprecation requires a release note and at least two minor
releases of overlap. Removing a stable field or version requires a new major
wire version.

The public conformance binary embeds validation, fingerprint, cache-key, and
execution-backend vectors. Compatibility changes must update implementation
code while preserving already shipped fixtures; historical fixtures must not
be rewritten to make a breaking change pass.

### Plugin frame size

The v1 host and Go/Python/JavaScript/TypeScript/C# SDKs bound each JSON message
body to **512 KiB (524,288 bytes)**, measured after UTF-8 encoding; headers are
separate from that body limit. Larger results must be split or represented by
bounded artifact references. The non-Go SDKs previously allowed 1 MiB, which
could produce responses the Go host refused. They now enforce the existing
host boundary on both reads and writes. No wire fields or Content-Type changed.

`platform-factory-conformance plugin EXECUTABLE` sends a complete, valid request
of 524,289 body bytes to verify rejection at that boundary. A truncated body
does not establish that an implementation enforces the size limit.

Publication targets use the same rule. Run
`platform-factory-conformance publication` to validate OCI Registry reference
normalization/rejection and the exact digest-pinned, hardened Kubernetes
Service/Job manifests emitted by the CLI. These vectors exercise the shared
target implementation rather than a test-only copy. They are deterministic
and require neither Registry credentials nor a Kubernetes cluster; live
interoperability remains covered separately by the Registry and Kind suites.

## Project files

`pf.yaml` and `pf.lock` are the default project filenames. Operators who need
the self-describing names can choose `pf init --filename-style long`, which
creates exactly `platform-factory.yaml` and `platform-factory.lock`. A project
has one style and one source of truth: discovery refuses multiple configuration
files or both lock spellings instead of choosing silently by priority. Both
styles carry an integer `version`; project manifests remain v1, while newly
generated locks are v2. Lock v1 remains readable for migration. Readers reject unknown fields, multiple documents, malformed
pins, and versions newer than the running CLI. A schema change that cannot be
expressed with optional fields requires a version bump and an automated
forward migration. Older CLI releases are never expected to guess the meaning
of a future schema.

Published closed JSON Schema 2020-12 documents live at
[`schemas/pf-v1.schema.json`](../schemas/pf-v1.schema.json) and
[`schemas/pf-lock-v1.schema.json`](../schemas/pf-lock-v1.schema.json). The
current lock contract is published separately as
[`schemas/pf-lock-v2.schema.json`](../schemas/pf-lock-v2.schema.json); v1 was
not modified in place. YAML
editors may apply the project schema after YAML parsing because the wire field
model is identical. Tests compare every published property with the Go wire
types and pin the schema-file SHA-256; changing v1 in place fails CI and
requires either a provably compatible intentional fixture update or a new
schema version plus migration.

`pf init` writes the selected pair atomically as one reviewed plan. Normal
project discovery validates the matching adjacent lock (`pf.lock` or
`platform-factory.lock`) before build, run, launch, publish, or deploy can
consume the project. The lock may be absent on legacy projects. A v2 lock
binds the semantic manifest through `plan_digest`; reformatting and comments
are neutral, while a value change blocks the build before any user command
runs. `pf freeze` atomically refreshes SHA-256 pins for source and explicitly
categorized toolchain files. Base-image and MicroVM boot pins use their
dedicated v2 fields. When present, the lock is authoritative and invalid input
fails closed.

## CLI machine output

PF-owned JSON envelopes use `api_version:
platform-factory.dev/cli-output/v1`. The initial stable envelope coverage is
enforced for detect, layout/archive verification, build (including dry-run),
pipeline plan/run and immutable Registry inspection. Existing fields remain at
the top level; the version field is additive. Pipeline output uses
`pipeline_api_version` for the embedded document's wire version so it cannot
be confused with the CLI envelope. Standard OCI and SBOM documents retain
their standard schema identifiers. Commands not yet carrying this envelope
remain explicitly outside the global stable-output claim until migrated and
covered by historical fixtures.

## Language inspection contract

The plain-executable language plugins now emit
`api_version: platform-factory.dev/language-inspection/v1` from `inspect`.
This is separate from the RPC plugin framing contract. Existing unversioned
inspection responses remain readable; an explicitly unknown version is rejected.
Existing fields retain their meaning and new fields must be optional. Removing
or changing a field requires a new major wire version and a migration path.

Frozen legacy and v1 fixtures live in `sdk/langplugin/testdata`; the SDK tests
read them through the real inspection process boundary. The CI offline CLI
check executes all eight shipped plugins and the dependency-layer paths of
Ruby, PHP, Java, .NET and Rust. Those checks do not execute their package managers
or establish production support for their runtimes. `--root` is resolved to an
absolute path before a package manager changes its working directory.

Dependency layer prefixes must be relative local paths, without backslashes,
colons or NUL bytes. A layer source must be a real directory; an output inside
that directory is refused. A complete tar is staged and renamed into place
only after successful writing, so failures preserve an existing layer.

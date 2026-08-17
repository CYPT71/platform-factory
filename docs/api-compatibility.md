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
in `internal/runtime`. Product code must depend on the SDK contracts instead
of importing an implementation package.

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
styles carry an integer `version`; version 1 is stable. Readers reject unknown fields, multiple documents, malformed
pins, and versions newer than the running CLI. A schema change that cannot be
expressed with optional fields requires a version bump and an automated
forward migration. Older CLI releases are never expected to guess the meaning
of a future schema.

Published closed JSON Schema 2020-12 documents live at
[`schemas/pf-v1.schema.json`](../schemas/pf-v1.schema.json) and
[`schemas/pf-lock-v1.schema.json`](../schemas/pf-lock-v1.schema.json). YAML
editors may apply the project schema after YAML parsing because the wire field
model is identical. Tests compare every published property with the Go wire
types and pin the schema-file SHA-256; changing v1 in place fails CI and
requires either a provably compatible intentional fixture update or a new
schema version plus migration.

`pf init` writes the selected pair atomically as one reviewed plan. Normal
project discovery validates the matching adjacent lock (`pf.lock` or
`platform-factory.lock`) before build, run, launch, publish, or deploy can
consume the project. The lock may be absent on legacy projects,
but when present it is authoritative and invalid input fails closed.

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

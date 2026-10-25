# GitLab OCI and microVM CI

`pf ci gitlab --output .gitlab/pf` vendors the component, runner and outbound
trigger source into a **new** directory. The CLI embeds the same reviewed
sources; `pf ci run` does not execute a runner downloaded from job artifacts.
The workflow builds normally, assembles the resulting verified OCI filesystem
into an initramfs, then boots that candidate through native KVM or Apple
Virtualization Framework. It does not build inside the VM.

## Runner prerequisites

Use a trusted builder image pinned by its actual registry SHA-256, containing
`pf`, Python 3, the target compiler and dependencies. Build and package jobs use
an unprivileged container executor with no release keys or registry credentials.
The sample Go build requires `CGO_ENABLED=0`, `GOOS=linux` and a `GOARCH` matching
the configured platform. Vendor dependencies before freezing for offline builds.

Boot requires a dedicated, **protected**, disposable runner with host-installed
`pf` and Python 3: Linux/amd64 with `/dev/kvm`, or macOS/arm64 with an entitled
`pf` binary. macOS signing uses the repository's
`scripts/microvm/hvf.entitlements`. Linux/arm64 direct boot is unsupported.
There is no emulation fallback or successful skip when hardware is unavailable.
Do not assign this runner to untrusted repositories. Protected-reference rules
alone do not replace GitLab's protected-runner settings.

## Prepare and review locks outside CI

Use `pf init` for a new project, or copy and adapt
`examples/gitlab-microvm/pf.yaml` in an existing Go module. Finalize the manifest,
then generate and review locks **outside CI**:

```sh
pf project freeze --config pf.yaml
pf ci plan-lock --config pf.yaml --output pf.lock.new
# Review pf.lock.new; install it as pf.lock only after reviewing the change.
```

`plan-lock` creates a new v2 semantic project lock and refuses overwrites. Its
scope is static OCI projects without additional files; microVM kernel and PID 1
pins are separately required in the CI config below. If no previous `pf.lock`
exists, `--output pf.lock` creates it directly. After a manifest change, generate
a new lock for review before replacing the old one. CI never regenerates locks.
The candidate contains only the static application; UID 65532 is required,
additional includes and embedded project trees are rejected. The trusted PID 1
is added only when packaging the guest, not to the OCI application image.

Put reviewed Linux kernel and Linux PID 1 binaries in `boot/kernel` and
`boot/init`, respectively. Build PID 1 from `cmd/microvm-init` for the guest
architecture. Obtain their SHA-256 values locally and set `pf.ci.json`:

```json
{
  "version": 1,
  "project": "pf.yaml",
  "inputs_lock": "pf.inputs.lock.json",
  "layout": "dist/oci",
  "output": "dist/microvm",
  "platform": "linux/amd64",
  "kernel": "boot/kernel",
  "kernel_digest": "sha256:REPLACE_WITH_REVIEWED_KERNEL_DIGEST",
  "init": "boot/init",
  "init_digest": "sha256:REPLACE_WITH_REVIEWED_INIT_DIGEST",
  "ready": "PF_APPLICATION_READY"
}
```

Then generate the recursive lock, selecting **all** build inputs (include your
vendor tree, package lockfiles, scripts and tool configuration when present):

```sh
pf ci lock --path src --path go.mod --path pf.yaml --path pf.lock \
  --path pf.ci.json --path .platform-factory/freeze.lock.json --path boot \
  --output pf.inputs.lock.json
pf ci verify --lock pf.inputs.lock.json
pf ci check --config pf.yaml --distroless
```

Commit the reviewed source, configuration and locks. Locks record SHA-256,
size, executable status, file type and recursive directory membership, including
empty directories. Symlinks, special files, traversal, overlapping roots and
excessive inventories are rejected. Verification runs before and after each
phase and requires the configurations and boot inputs to be locked. Limits:
128 roots, 20,000 entries, 256 MiB per file, 1 GiB total, 8 MiB lock JSON.
These are drift checks, not OS file locks or an immutable sandbox. They cannot
detect a transient modification restored between checks. CI authors must include
all inputs; network dependency resolution is not a reproducible-input proof.

## Include the component

In the consuming project's `.gitlab-ci.yml`:

```yaml
include:
  - local: '/.gitlab/pf/component.yml'
    inputs:
      builder-image: 'registry.example/team/pf-builder@sha256:REPLACE_WITH_ACTUAL_DIGEST'
      build-runner: 'linux'
      boot-runner: 'pf-kvm'
variables:
  CGO_ENABLED: '0'
  GOOS: 'linux'
  GOARCH: 'amd64'
```

Replace the illustrative registry digest; the component rejects floating tags.
Run from repository root. `dist/oci` and `dist/microvm` are fixed artifact paths;
outputs must be absent initially and outside locked sources. Stages are `build`,
`package`, `boot`; merge these stage names if adding other jobs. Boot is required
on protected refs; merge-request pipelines intentionally only build and package.
Each job has a fresh checkout and receives artifacts through explicit `needs`.
Only generated `dist/` is uploaded; never store credentials there.

To execute the same phases locally:

```sh
pf ci run --config pf.ci.json --phase build
pf ci run --config pf.ci.json --phase package
pf ci run --config pf.ci.json --phase boot
```

`package.json` binds the OCI manifest/rootfs, kernel, PID 1, initramfs, platform
and input-lock digests. `boot.json` records a successful readiness smoke test
and the package evidence digest. A missing readiness marker, mismatched digest,
unsupported host or boot error fails the job. A 45-second native boot deadline
bounds the smoke test. An application readiness message is not a long-running
health or isolation certification.

## Outbound lifecycle hook

Use a fixed GitLab origin, numeric project and ref in an absolute owner-only
configuration file outside the checkout, for example `/trusted/gitlab.json`:

```json
{"version":1,"url":"https://gitlab.example.com","project":42,"ref":"main","events":["command.failed"]}
```

`chmod 600` this file. Supply `PF_GITLAB_TRIGGER_TOKEN` through the existing
hook configuration's `pass_env`, and configure the callback command as:

```json
["/trusted/bin/pf","hooks","gitlab","--config","/trusted/gitlab.json"]
```

The existing lifecycle dispatcher supplies the event JSON on stdin. The trigger
uses GitLab's pipeline trigger API over verified HTTPS, fixes the target ref,
disables redirects and proxies, forwards only a validated nonsecret event ID,
never logs the token, and performs no retry. A timeout can leave a remote
pipeline running: inspect GitLab before retrying. Events emitted by `ci`,
`hooks`, or a process with `CI=true` do not retrigger pipelines. This hook does
not itself receive public inbound webhooks or create fixes/PRs; the existing
isolated autofix hook is a separate callback.

API reference: [GitLab pipeline triggers](https://docs.gitlab.com/ci/triggers/).
Component syntax: [GitLab CI inputs](https://docs.gitlab.com/ci/inputs/).

## Evidence and release boundary

Tests cover input drift, symlink escape, strict schemas, protected boot rules,
packaging and secret placement. A local ARM64 application was built into a
static OCI layout, packaged and booted using Apple Virtualization Framework,
with its literal application readiness marker observed.
No GitLab instance/project/token was configured in this workspace, so live
GitLab lint and a hosted GitLab execution have **not** been verified.

These artifacts are unsigned candidates. Publishing and promotion must use the
existing `pf publish` / `pf verify-release` signature, SBOM, provenance and policy
checks in a separate protected release job with independently pinned trust keys.
This component does not certify reproducibility across fresh checkouts: project
creation timestamps still depend on filesystem metadata. Distributed HA, CAS
source materialization, tenant quotas and hostile-tenant isolation require their
own deployment-specific validation; this addition does not certify the entire
platform for production.

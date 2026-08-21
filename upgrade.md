# Écarts restants du parcours Junior défini par `Pf-init.md`

Status: implementation and junior-perspective retest completed against the real
CLI on 2026-08-11.

`Pf-init.md` est la source de vérité produit et le parcours Junior est son test
d'acceptation. Ce fichier ne définit pas une expérience parallèle : il conserve
uniquement les écarts restant à fermer pour satisfaire entièrement
`Pf-init.md`.

Architecture correction verified on 2026-08-11: language detection no longer
lives in `cmd/` or `internal/`. The relevant language plugin must be loaded;
`pf init` invokes its `inspect` capability. Real empty-directory experience
tests pass for Go, Python, Node.js and C#/.NET.

## Target outcome

A junior developer with a new Python application should be able to use the
three product verbs without understanding OCI layouts, runtime-layer assembly,
SBOM file locations, provenance predicates, registry digests, or handwritten
Kubernetes manifests:

```text
pf init
pf build
pf publish
```

Platform Factory may ask questions and must explain recommendations, but it
must never report an unverified capability as valid.

The Go Junior path is now proven with `pf init --engine docker|podman` followed
by a single `pf launch` against both real local engines: PF builds a Linux ELF,
writes/verifies OCI, retains the local layout, imports it, runs it, and observes
the Hello World output. Python remains blocked on a
verified Linux runtime provider. Intermediate plugin creation is proven in Python,
JavaScript, TypeScript, PHP and C#. Senior multi-architecture,
reproducibility, compose, pipeline and policy primitives are proven; unified
fleet-level orchestration remains an open gap.

## Review fixture

The review started from a directory containing only:

```text
hello-python/
└── app.py
```

with:

```python
print("hello from python")
```

The tested host had Python, Docker and Podman installed. Both local container
engines were reachable and passed the real Hello World launch test. A missing
engine still produces a visible skip/preflight rather than a false success.

## Guided terminal experience

Interactive `pf init` now renders a three-step, plugin-driven terminal flow:
language choice, entrypoint confirmation, then a complete review before write.
It has no hard-coded language catalog, works without ANSI color, defaults to a
safe cancellation, marks known/created/unknown information distinctly, and
states that init itself performs no build or deployment. `--yes` and nil stdin
remain deterministic automation paths.

## Continuous acceptance

`.github/workflows/ci-pf-init-experience.yml` validates the empty-repository
personas and TUI, then runs the same real `pf launch` acceptance test in a
Docker/Podman matrix. The root `go test ./...` suite also passes locally.

## Retest from an empty repository

The automated review builds the real `pf` binary and the real Python language
plugin, gives the plugin its own empty managed-plugin directory, and starts the
application repository empty. It then writes only `app.py` and invokes CLI
subprocesses exactly as a junior user would.

Verified sequence:

```text
pf init --dry-run .
pf init --yes .
pf build --dry-run
pf build
pf publish --dry-run --allow-incomplete-evidence registry.example/hello:v1
pf deploy --dry-run registry.example/hello@sha256:<review digest>
```

Observed result:

- init detects Python through the loaded Python plugin and records
  `profile: python` in `pf.yaml`;
- dry-run writes nothing, and real init adds only `pf.yaml` and `pf.lock`;
- build dry-run now exits non-zero with structured `"valid": false` because no
  verified Linux Python runtime provider is configured;
- real build stops at the identical capability preflight instead of failing
  later inside OCI assembly;
- the error explains that PF refuses to package the host runtime into a Linux
  image and points to `pf doctor` plus the runtime-provider decision;
- publish now accepts only the image destination and discovers the initialized
  project's OCI layout; in this review it correctly cannot publish because the
  blocked build produced no layout;
- deployment dry-run from the initialized no-port project selects a hardened
  Kubernetes `Job`, explains the choice, and emits no `containerPort`;
- the deployment manifest was reviewed using a synthetic digest solely to
  exercise the read-only manifest path. No live deployment was claimed.

## What works today

### `pf init`

After loading the Python plugin, `pf init --dry-run .` correctly reports:

- language `python`, with `app.py` as evidence;
- no external dependencies;
- `app.py` as the artifact/entrypoint;
- container as the recommended runtime;
- only `pf.yaml` and `pf.lock` as planned files.

`pf init --yes .` writes only those two PF files. The resulting configuration
is compact and contains no invented `requirements.txt` or package manager.

### Deployment safety primitives

- Publishing requires confirmation or dry-run.
- Production publication requires SBOM, signature, provenance, policy, and
  evidence unless the development escape hatch is explicit.
- Kubernetes images must be pinned by SHA-256 digest.
- Kubernetes dry-run prints a hardened manifest without applying it.
- Live deployment uses `kubectl apply`, watches rollout status, and has an
  idempotency journal.

These are strong low-level primitives, but they are not connected into a
junior-friendly project workflow yet.

## Observed broken journey

| Step | Observed result | Review |
|---|---|---|
| `pf init --dry-run .` | Correct detection and two-file plan | ✅ Good |
| `pf init --yes .` | Valid compact `pf.yaml` and minimal `pf.lock` | ✅ Good |
| `pf build --dry-run` | Reports `"valid": false` and the missing verified Linux runtime | ✅ Safe, still blocked |
| `pf build` | Stops at the same runtime capability preflight | ✅ Safe, still blocked |
| `pf publish --dry-run IMAGE` | Discovers project layout automatically; none exists after blocked build | ✅ Connected, still blocked |
| `pf publish --dry-run --allow-incomplete-evidence ...` | Fails because no OCI layout exists | Expected consequence |
| `pf deploy --dry-run IMAGE:tag` | Rejects mutable tag and requires digest | ✅ Safe but unexplained handoff |
| `pf deploy --dry-run IMAGE@sha256:...` | Emits and explains a hardened no-port Job | ✅ Correct preview |

Additional usability findings:

- Top-level help presents low-level and project workflows together without a
  guided “start here” path.
- `pf publish IMAGE` now discovers the project output; the explicit
  `pf publish LAYOUT IMAGE` expert form remains supported.
- `pf deploy` does not consume `pf.yaml` or the digest returned by publish.
- A project with no declared ports now previews as a Kubernetes Job with no
  invented port. `--workload service|job` provides an explicit override.
- No Service is emitted for applications that really are network servers.
- Build does not automatically produce the SBOM, provenance, signature, policy
  evidence, and immutable digest that production publish requires.
- `pf doctor` finds the unavailable runtimes and cluster, but the build/publish
  commands do not perform a scoped preflight or link to its remediation.
- A project directory name can become the default image name without enough
  validation or a friendly prompt for a publishable name.

## P0 — make the three-verb path truthful and complete

### 1. Resolve and prove the Python Linux runtime during `pf init`

`pf init` must distinguish application entrypoint from runtime entrypoint:

```yaml
build:
  language: python
  entrypoint: app.py
  dependencyManagement:
    mode: none

runtime:
  mode: container
  provider: <verified Linux runtime provider>
```

The runtime provider may be a signed PF language plugin, a digest-pinned
toolchain image, or a remote Linux builder. A native macOS Python executable
must never be packaged into a Linux OCI image.

Acceptance criteria:

- `pf init` records a selected, verified runtime provider or leaves an explicit
  unresolved decision.
- `pf build --dry-run` is invalid when no provider can satisfy the target
  platform.
- The real build uses `/usr/bin/python3` (or another verified runtime path) as
  OCI entrypoint and `/app/app.py` as its application argument.
- The source file is included once, with no duplicate OCI destination.
- Cross-platform runtime artifacts are digest-pinned in `pf.lock`.

### 2. Make `pf build --dry-run` an honest capability preflight ✅ partial

Dry-run now rejects freshly initialized interpreted-language projects when no
verified Linux runtime provider is selected. Toolchain, registry-evidence and
target-platform preflights still need to be unified under the same report.

Acceptance criteria:

- It checks target platform, runtime provider, toolchain, dependency state,
  output ownership, and required plugin capabilities.
- It never prints `"valid": true` if the equivalent real build is known to
  fail before reading application code.
- Failures name the missing capability and give one actionable command or
  configuration choice.
- Dry-run writes no project, cache, credential, or runtime state.

### 3. Make `pf build` produce the complete release bundle

A successful build should return one structured result containing:

- verified OCI layout;
- immutable image digest;
- SBOM;
- SLSA provenance;
- signature or an explicit signing-ready state;
- policy evidence;
- reproducibility report.

All artifacts must be addressed by digest and stored in system build/cache
locations unless the user explicitly requests a project-local export.

Acceptance criteria:

- Production `pf publish` consumes build outputs directly; the junior does not
  locate or author evidence files.
- Unresolved dependency or runtime decisions fail before executing a build
  command.
- A retry after interruption resumes safely and cannot create conflicting
  output state.

### 4. Connect publish to the initialized project ✅ discovery complete

Target experience:

```text
pf publish ghcr.io/acme/hello-python
```

Acceptance criteria:

- With no layout argument, publish discovers the nearest `pf.yaml` and the
  verified build result.
- It refuses stale, missing, unsigned, or policy-failing build evidence.
- Dry-run shows source digest, destination repository, artifacts, credentials
  source (never the secret), and registry mutations.
- Success returns and persists the immutable published reference required by
  deployment.
- The existing advanced layout-oriented interface remains available under an
  explicit expert/OCI command surface.

### 5. Connect deployment to the published digest

Target experience:

```text
pf publish --target kubernetes
```

or, if deployment remains a separate command:

```text
pf deploy --target kubernetes
```

Acceptance criteria:

- The command consumes the digest produced by publish; users do not copy it.
- A tag is never substituted for a digest.
- It performs client validation, Kubernetes server-side dry-run, and diff
  before asking for confirmation.
- It shows the current context, namespace, resource ownership, and rollback
  point.
- An unreachable cluster fails during preflight without claiming a deployment
  operation.

## P1 — model what is being deployed

### 6. Ask for workload type instead of assuming Deployment ✅ preview complete

For the reviewed `print(...)` application, PF cannot prove a long-running
server or a port. It should recommend a Job and explain why.

The Kubernetes preview now does this for an initialized project with no
declared ports and supports `--workload service|job`. Persisting the workload
decision during init and generating a Service for network workloads remain.

Supported choices should include:

- service/Deployment;
- one-shot Job;
- scheduled CronJob;
- local container run;
- MicroVM where its required capabilities are proven.

Acceptance criteria:

- No detected listener means no invented port 8080.
- A service workload emits Deployment plus Service.
- A one-shot script emits Job and no Service.
- The durable choice is recorded in `pf.yaml` and is not silently reconsidered
  by build or publish.

### 7. Add publish and deployment targets to `pf.yaml`

The initializer should offer, not invent:

```yaml
publish:
  repository: ghcr.io/acme/hello-python

deploy:
  target: kubernetes
  context: kind-dev
  namespace: default
  workload: job
```

Context and credentials remain external references; secrets must never be
written into the project.

### 8. Validate and explain project identity

Acceptance criteria:

- `pf init` proposes a DNS/OCI-safe project name derived from the directory.
- Invalid or hidden-directory names require correction before writing.
- The same stable name drives the image, Kubernetes labels, workload state,
  and rollback identity.

### 9. Make `pf doctor` task-scoped

Add scopes such as:

```text
pf doctor build
pf doctor publish
pf doctor deploy --target kubernetes
```

Each mutating command should automatically run the relevant non-mutating
checks and summarize only blockers for that action.

## P2 — complete the junior operational loop

- `pf status` should combine build, registry, and rollout state.
- `pf logs` should select the initialized workload without requiring raw
  Kubernetes names.
- `pf rollback` should preview and restore the last known digest.
- `pf publish --target local` should support Docker/Podman for learning without
  a registry or cluster.
- A local Kubernetes target should guide users through kind/minikube only when
  they explicitly select it; PF must not install or start infrastructure
  silently.
- Error messages should end with the next safe command, not only a low-level
  validation error.

## Required end-to-end acceptance test

Run the real release binary, not an in-process mock:

1. Create a temporary directory outside any parent Git repository.
2. Write only `app.py` with standard-library code.
3. Run `pf init --dry-run`; assert zero writes and a Job/no-port recommendation.
4. Run `pf init`; assert only `pf.yaml` and `pf.lock` were added.
5. Run `pf build --dry-run`; assert every required capability is proven.
6. Run `pf build`; verify OCI layout, SBOM, provenance, signature, policy
   evidence, and digest.
7. Run publish against an ephemeral authenticated registry; assert artifacts
   are linked to the subject digest.
8. Run Kubernetes server-side dry-run and diff against a disposable kind
   cluster.
9. Deploy the digest, wait for Job completion, collect logs, and verify
   `hello from python`.
10. Re-run every mutation and simulate interruption; assert idempotent recovery
    and no duplicate registry uploads or Kubernetes resources.
11. Exercise rollback and verify the previous digest is restored.

The test must skip only when an explicitly named external capability is
unavailable. A skip is not a pass and must remain visible in release evidence.

## Definition of done

The junior path is complete only when a user can move from `app.py` to a
running, digest-pinned workload using the three documented verbs, with every
security artifact generated automatically, every mutation previewed, and no
step requiring knowledge of PF's internal file layout.

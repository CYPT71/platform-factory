# Clean Architecture refactor: status and next stage

Full original plan (context, dependency rule, verification strategy) is
at `/Users/cyprien/.claude/plans/zany-fluttering-frost.md`. This file is
the actionable, evidence-based backlog for continuing the work.

## Repo-wide pass: every cmd/ binary, plus api/sdk boundary hardening

After the `cmd/platform-factory`-scoped work below was declared done, a
follow-up pass extended the same audit to the rest of the repo, since
`cmd/platform-factory` was only ever one of ~13 `cmd/` binaries plus a
pre-existing `api`/`sdk` boundary gap:

- **archtest generalized, then the remaining exception eliminated
  entirely**: the api/internal-independence rule (previously checked
  only `api/migration`) now covers all of `api/*`, unconditionally.
  It first landed with `api/oci` as one documented exception (`sdk/*`
  can never depend on `internal/*`, so something in the api/sdk
  boundary had to bridge to `internal/oci`'s real algorithm), but on
  review that exception was itself removable, not just explainable:
  `internal/oci` (the build engine - `builder.go`, `buildconfig.go`,
  `elf.go`, `extralayers.go`) was renamed to a plain top-level `oci`
  package (matching the existing `conformance` package's precedent - a
  non-internal package may depend on `internal/*` freely, just not the
  reverse), so `api/oci/v1` now imports the same non-internal path
  every other api/sdk pair already used. `apiInternalExceptions` is
  gone from `internal/archtest/archtest.go` entirely - there are no
  exceptions left. `internal/oci/dockersave` (genuinely internal-only,
  no api/sdk consumer) moved to `internal/dockersave` rather than
  staying nested under a directory that no longer has its own package.
  16 Go files' imports, `domainInfrastructureBoundaries`' five
  `"internal/oci"` targets, ~20 doc comments, `CODEOWNERS`, and 5 CI
  workflow files all updated to match.
- **Every other `cmd/` binary audited**, extracted where it had real
  business logic, left alone with a documented reason otherwise:
  - `microvm-initramfs` → `internal/app/microvminitramfs` (rootfs
    convert/pack/install orchestration).
  - `platform-factory-plugin-languages` → `internal/app/languageplugin`
    (per-language freeze command table).
  - `platform-factory-packager` → `internal/app/packager` (the entire
    archive-assembly algorithm - zero prior internal/ delegation).
  - `platform-factory-installer` → `internal/app/installer` (component
    catalog/selection/build-step planning; the file's own doc comment
    already said this was "kept separate... to stay unit-testable" -
    formalized across the internal/cmd boundary instead of just a
    same-package file split).
  - `platform-factory-control-plane`: `verifyCompletionProvenance`
    moved to `internal/control.ControlPlane.VerifyCompletionProvenance`
    - the rest of `server.go` was already a clean HTTP adapter over
    `internal/control` ("coordinates workers and leases independently
    of transport", per that package's own doc comment).
  - `platform-factory-worker` → `internal/app/workerpipeline` (pipeline
    lease payload validation/CAS-blob-pulling/execution; `worker.go`
    itself - the HTTP transport/polling/heartbeat client - stayed,
    genuinely transport-coupled).
  - Audited, no extraction (each already thin, or categorically not a
    fit): `oci-builder`, `platform-factory-conformance`,
    `platform-factory-runtime` (a platform-gated OCI-runtime CLI facade
    already delegating everything to `internal/ociruntime`),
    `example-service` (a deliberate stdlib-only test fixture),
    `platform-factory-plugin-demo` (a deliberate sdk-only reference
    plugin), `microvm-init` (a guest-side PID 1 - genuine OS-process-
    supervision code with no higher-level "domain" to extract into; it
    already uses its one real internal/ dependency,
    `internal/guesttransport`, correctly).
  - `plugins/*` (the external plugin modules in `go.work`) were **not**
    touched - they're already the most strictly isolated layer, with
    their own archtest rule forbidding them from depending on
    `internal/*` at all. Extracting into `internal/app/*` would be
    backwards for them.
- **Repo cleanup**: `platform-factory` (the built binary) and
  `examples/sdk/plugin-csharp-lazy-docker/{bin,obj}/` were dangling,
  uncommitted, accidentally-tracked build output; 8 plugin binaries
  (`plugins/lang-*/bin/platform-factory-lang-*`, ~17MB) were already
  committed to HEAD despite an existing (but path-mismatched)
  `.gitignore` pattern. Fixed `.gitignore` and untracked all of them
  (still present on disk, just no longer tracked).

Every step in this pass ran the same gate as the work below (`go
build`, `go vet`, `go test ./internal/archtest/... ./...`, `GOOS=linux`
cross-build) and is committed. This closes the "out of scope" note that
used to sit here - there is no remaining unaudited `cmd/` binary in this
repo.

## Done: Phases 1-11 (all originally-planned phases complete)

1. **`cmd/tui/kit`** - shared TUI styling/program-launch/key-handling.
   `buildtui`, `runtimetui`, `marketplacetui` refactored onto it, zero
   public API/behavior change.
2. **`internal/oci/dockersave`** - `import.go`'s OCI-layout <-> Docker-
   Save transposition. Only `PrepareContainerImage`/`WriteDockerArchive`
   exported.
3. **`internal/app/provisionruntime`** - `provisionruntime.go` +
   `imagepull.go`. `Service` struct; `LangpluginResolve` left unset by
   `New()`, wired by cmd (`sdk/langplugin.Resolve`).
4. **`internal/app/pipeline`** - `pipeline.go`'s decode/plan/run/journal
   logic. `Plan`/`Run`/`Decode` methods (the last added because
   `evidence.go` also needed pipeline decoding).
5. **`internal/app/publish`** - `evaluatePublicationPolicy`,
   `nativePublicationArtifacts`, `transitionPublishWorkload`. The
   operation-journal/claim/pushOCI machinery turned out to be shared
   with deploy/rollback/microvm/launch/observe/status - stayed in cmd,
   not moved.
6. **`internal/app/deploy`** - `evaluateDeploymentPolicy`,
   `parseKubernetesExtensions`. Rollback had no separable business logic
   of its own (pure orchestration over shared cmd infra) - no
   `internal/app/rollback` package was created.
7. **`internal/app/plugin`** - `prepareSource`/`buildGoPlugin`/
   `buildDotnetPlugin`/etc. The `sdk/langplugin`-calling orchestration
   (`runPluginLoad`, `runPluginUnload`, `runPluginList`, `runPluginCreate`)
   stayed in cmd per the no-sdk rule.
8. **`internal/app/marketplace`** - `marketplacePaths`,
   `anyReleaseVerified`, `splitNameVersion`, `loadMarketplaceKeys`. Most
   of `marketplace.go` was already thin over `internal/marketplace`. The
   `tui` subcommand is excluded on purpose (see below).
9. **`internal/app/project`** - `projectNeedsRebuild`,
   `projectRequiresFrozenInputs`, `validateBuildDAG`,
   `validateBuildCapability`, `projectProfile`, `watchContainerName`.
   The CLI orchestration (`runProjectContext`, `runConfiguredProject*`,
   `buildProjectContextWithBudget`) stayed in cmd - too entangled with
   `*pluginHost`/`projectExecutor`/`buildtui` to move safely in one pass.
10. **`internal/app/build`** - consolidated `main.go`'s `runBuild` and
    `project.go`'s `buildProjectContextWithBudget`, which had been
    maintaining **two separate copies** of `buildTarget`/`buildSettings`/
    `writeSBOMToDist`/`writeBuildEvidence`/`parsePlatform`. Both cmd
    files now call the same package - a genuine dedup, not just an
    extraction. `writeReportJSON` stayed duplicated (also used by
    `lifecycle.go`, an unrelated domain).
11. **`internal/app/microvm`** - just `Capability`/`Params`/`Result`
    (the KubeVirt wire contract), per mvp.md's own deferral of the
    native-KVM-eligibility hardware-probing logic.

## Critical lesson from Phase 11: use `internal/archtest`, not manual greps

**The repo already has a formal, comprehensive architecture-enforcement
test at `internal/archtest`** (`go test ./internal/archtest/...`,
included in `go test ./...`). It was discovered only when it caught a
real violation: `internal/app/microvm/microvm_sdk_test.go` imported
`sdk/plugin` to cross-check a hardcoded constant, which is forbidden -
**the no-sdk/no-api rule applies to test files too**, not just
production code.

`internal/archtest/archtest.go`'s `forbiddenReason` already enforces,
for every `internal/*` package generically (this covers all of
`internal/app/*` automatically, no special-casing needed):
- no `api/*` or `sdk/*` imports (production code **or tests**)
- no `cmd/*` imports (this also covers `cmd/tui/*`, since it's a
  subpath of `cmd/`)
- plus domain-specific infrastructure boundaries (see
  `domainInfrastructureBoundaries` in that file) and a migration-domain
  allowlist

**This makes the "3 guardrail greps" this session ran by hand after every
phase entirely redundant.** From now on, just run:
```
go test ./internal/archtest/... ./...
```
as part of every phase's gate - it's authoritative, already covers
`internal/app/*` with zero extra setup, and would have caught the
microvm mistake immediately instead of only on the final full-suite run.

## New stage: strict Clean Architecture across all of `cmd`, hardened via interfaces

Two goals for the next session:

### 1. Finish extracting the remaining `cmd/platform-factory` business logic

Current sizes (post Phase 1-11), business-logic files not yet touched at
all this session:

| File | Lines | Status |
|---|---:|---|
| `main.go` | 1307 | Partially done (build block extracted); `runCompose`, `runImport`, `runInspect`, dispatch/alias logic untouched |
| `lifecycle.go` | 978 | Partially done (publish/deploy policy+artifacts extracted); the operation-journal/claim/push orchestration itself is still inline (deliberately - shared with rollback/microvm) |
| `project.go` | 923 | Partially done (staleness/capability extracted); `runConfiguredProject*`, `buildProjectContextWithBudget`, `migrateProject`, `planProject`, `freezeProject` still inline |
| `marketplace.go` | 549 | Partially done; `runMarketplaceSync`/`Search`/`Install`/`Publish` bodies still inline (thin already, low remaining value) |
| `init.go` | 397 | Audited, blocked - already delegates to `internal/app/projectinit`; its own three local helpers (`detectionFromPlugins`, `selectLanguageInspection`, `applicationInspectionFromPlugin`) take `langplugin.Inspection` (an `sdk/langplugin` type) as a parameter, which makes them the sdk-to-domain translation boundary itself - exactly the piece the no-sdk rule requires to live in cmd, not internal/app/* |
| `microvm.go` | ~380 | Partially done; `InspectLegacyDisk` moved to `internal/app/microvm` (see below). `runNative`, `runMicroVM` dispatch still inline (deeply coupled to `*pluginHost`/`microVMExecutor`, per the package doc comment - left as-is) |
| `plugin.go` | 347 | Partially done |
| `launch_publish.go` | ~215 | Partially done; `ReproducibleBuild`/`WriteLaunchPublicationEvidence` extracted. `runLaunchPublish` itself (flag parsing, freeze/build/publish/run orchestration) stays - genuine CLI dispatch |
| `plugins.go` | 200 | Audited, blocked - `*pluginHost` imports `sdk/plugin` directly (aliased `api`); can never move into `internal/app/*` under the no-sdk rule, full stop |
| `init_ux.go` | ~205 | Audited; `filterResolvedUnknowns` moved to `internal/app/projectinit.FilterResolvedUnknowns` (pure, operates only on projectinit's own `Unknown` type). Everything else is genuine interactive TTY UX (panels, prompts, `readLine`) - stays |
| `status.go` | ~40 | Done - `internal/app/status` (see below) |
| `plugin_provenance.go` | 141 | Audited, no extraction - flag parsing + calls into already-factored `internal/provenance`/`internal/signing`; the one local helper (`readGoModulePath`) is single-use, already dependency-free, and not worth its own package |
| `init_legacydisk.go` | 139 | Audited, no extraction - interactive stdin prompting (`detectAndResolveLegacyDisks`/`promptForBootDisk`) interleaved with a thin wrapper over already-factored `internal/vmdisk`; the pure slice (`legacyDiskConfigFor`/`relativeTo`) is ~10 lines and not worth fragmenting out of its interactive context |
| `language_plugin.go` | 11 | Done - only `resolveLoadedPlugin` (sdk-calling wiring) remains |
| `observe.go` | ~40 | Done - only `runProjectObservation` (CLI glue) remains |

Already-thin via a pre-existing pattern (don't need new work, just
confirm during audit): `doctor.go`, `sbom.go`, `verify_release.go`
(→ `internal/app/{doctor,sbom,verify}`), `init.go` (→
`internal/app/projectinit`).

Progress this batch:
1. **Done** - `microvm.go`'s `runInspectLegacyDisk` →
   `internal/app/microvm.InspectLegacyDisk`. Returns
   `(InspectLegacyDiskResult, error)` and never prints (matches
   `internal/oci/dockersave`'s "results are returned, never printed"
   precedent) rather than taking `stdout, stderr io.Writer` - the two-
   tier exit-code split the CLI relies on (2 for a
   `BuildCompatibilityReport` failure, 1 for discovery/file-I/O
   failures) is preserved via a exported sentinel,
   `microvmapp.ErrCompatibilityReport`, that cmd checks with
   `errors.Is`. The "at least one --disk" check stayed in cmd (it's
   flag validation, not domain logic). No test changes needed - all
   coverage goes through `runMicroVM` at the CLI boundary and only
   asserts with `strings.Contains`, so the sentinel-wrapped error text
   (which still contains the original message as a substring) still
   matches. Full gate green (`go build`, `go vet`,
   `go test ./internal/archtest/... ./...`, `GOOS=linux` cross-build).

2. **Done** - `observe.go`'s `loadDeployedProject`/`deployedProject` →
   `internal/app/observe.LoadDeployedProject`/`DeployedProject`. Free
   function, no injectable surface. `decodeStrictJSON` duplicated
   locally again (same call as Phases 5/6/9). Three call sites updated:
   `observe.go` (`runProjectObservation`), `lifecycle.go` (rollback,
   imported bare as `observe` alongside the existing bare `deploy`/
   `publish` imports), `status.go` (`var deployed observe.DeployedProject`,
   `validKubernetesName`/`validDigestReference` cmd wrappers left
   untouched in `lifecycle.go` - still used by deploy/rollback flag
   validation beyond this struct). No test-migration risk (all coverage
   is CLI-boundary, via `runProjectObservation`/`runRollback`/`runStatus`
   with `strings.Contains`/JSON-decode assertions, confirmed via grep
   before moving). New `internal/app/observe/observe_test.go` covers
   valid/missing/invalid-identity cases directly. Full gate green.

Recommended order for the rest of this batch (smallest/most
self-contained first, matching this session's own methodology):
3. **Done** - `language_plugin.go`'s `languagePluginLayer`/
   `languagePluginDestPrefix`/`pluginResolver` → folded into
   `internal/app/project` (`LanguagePluginLayer`/
   `LanguagePluginDestPrefix`/`LanguagePluginResolver`), per the
   Phase 3/7 no-sdk precedent: `resolveLoadedPlugin` (the
   `sdk/langplugin.Resolve`-calling production resolver) is the only
   thing left in `cmd/platform-factory/language_plugin.go`, wired in by
   `project.go`'s call site exactly like `provisionruntime.Service
   .LangpluginResolve` was. The `execute` parameter is typed as an
   unnamed func literal, not a named type, specifically so cmd's named
   `projectExecutor` value is directly assignable without a wrapper -
   Go's assignability rule requires at least one side to be unnamed
   when the underlying types match. `formatCommand` duplicated locally
   again (third copy, after `lifecycle.go` and
   `provisionruntime.go`). All 7 test functions moved verbatim to
   `internal/app/project/language_plugin_test.go` (pure business-logic
   tests, reusing that package's existing `loadTestProject` helper,
   identically shaped to cmd's `loadProjectTest`) - `plugin_test.go`'s
   own `resolveLoadedPlugin` tests stayed in cmd untouched. Full gate
   green.
4. **Done** - `launch_publish.go`'s reproducibility/evidence logic →
   `internal/app/build` (`ReproducibleBuild`, `WriteLaunchPublicationEvidence`
   in new files `reproducible.go`/`publication.go`). The double-build
   itself still can't move - it calls cmd's own `buildProjectContext`,
   which is deeply `*pluginHost`/`projectExecutor`-entangled CLI
   orchestration (item 6 below) - so `ReproducibleBuild` takes the
   build step as an injected `func() (digest string, err error)`
   callback instead, matching the Service-with-injected-effects shape.
   The one wrinkle: `buildProjectContext` returns `(string, int)` (an
   exit code, not an error) and already prints its own detailed stderr
   message on failure, so a naive `error` conversion would lose the
   1-vs-2 exit code distinction `launch --publish`'s own tests rely on.
   Fixed the same way as item 1's `ErrCompatibilityReport`: cmd wraps
   the exit code in a local `buildFailureCode` error, `ReproducibleBuild`
   wraps *that* in its own exported `FailedBuild` (so callers can tell
   "build itself failed" apart from "the reproducibility workspace
   bookkeeping failed"), and cmd unwraps both with `errors.As` to
   recover the original code. `writeLaunchJSON` (cmd's shared atomic
   JSON writer, used well beyond this one call site) stayed in cmd;
   `WriteLaunchPublicationEvidence` got its own local
   `writeSensitiveJSON` copy (0o700/0o600, matching the original -
   deliberately stricter than `writeReportJSON`'s existing 0o755/0o644
   duplicate, since these documents gate a registry publication
   decision). `version` global threaded through as a `builderVersion`
   parameter, same as Phase 10's `WriteBuildEvidence`. No test changes -
   `reproducibleProjectBuild`/`TestLaunchPublish*` still call the same
   cmd-level signatures. (Pre-existing, unrelated gofmt drift noticed in
   `internal/app/build/build.go`'s `Settings` struct alignment - left
   alone, out of scope.) Full gate green.
5. **Audited, no extraction** - `main.go`'s `runCompose`/`runImport`/
   `runInspect`. All three are already thin: flag parsing + format
   selection + one call into an already-factored internal package
   (`layout.Compose`, `layout.Verify`/`VerifyArchive`/
   `dockerarchive.Verify`, `dockersave.PrepareContainerImage` from
   Phase 2) + JSON/text formatting. No business logic left worth
   moving - extracting would just relocate flag/print glue into
   `internal/app`, adding an abstraction with no payoff.
6. **Audited, no extraction** - `project.go`'s remaining orchestration
   (`resolveFreezeSteps`, `explainProjectAction`, `rebuildProjectLayout`,
   `runConfiguredProject`, `runConfiguredProjectWatch`,
   `watchForChange`, `stopWatchedContainer`, `buildProjectContextWithBudget`)
   and `lifecycle.go`'s (`runPublish`, `runDeploy`, `runRollback`,
   `runClaimedOperations`) were read in full to confirm or overturn the
   twice-deferred "highest-risk/lowest-value" call - **confirmed, not
   overturned**. Concretely:
   - `project.go`'s functions are inseparable from cmd-only process
     machinery: `*pluginHost` capability negotiation, goroutine/channel/
     `signal.NotifyContext`-based watch loops, and dispatch into
     `runMicroVM`/`runContainer`. This isn't business logic wearing a
     CLI costume - it *is* the CLI's process model. Moving it would
     mean either smuggling `cmd`-only types into `internal/app` (breaks
     the dependency rule outright) or re-deriving `*pluginHost` as an
     internal/app-level interface, which is a redesign of the plugin
     capability-negotiation system, not an extraction.
   - `lifecycle.go`'s functions are ~90% flag parsing, usage/error
     messages, and calls into already-extracted `internal/app/publish`
     (`EvaluatePolicy`, `BuildArtifacts`, `TransitionWorkload`) plus the
     operation-journal/workload-state-store/registry push-tag machinery
     Phase 5 already decided must stay in cmd (shared across publish/
     deploy/rollback/microvm, not owned by any single domain). Nothing
     left over qualifies as an undiscovered domain rule.
   - Conclusion: this item is correctly deferred, not under-resourced.
     Re-attempting it would either violate the dependency rule or
     produce a hollow extraction (relocating glue, not logic) - exactly
     what this refactor has avoided everywhere else. Leave it as cmd's
     CLI-adapter layer.

## Status: both "New stage" goals complete

Extraction backlog: 5 of 6 originally-listed items extracted real logic;
the 6th was twice deferred and confirmed (not just assumed) to be
correctly so. A follow-up pass then read every remaining "Untouched" row
in the file-size table above (`status.go`, `plugin_provenance.go`,
`init_legacydisk.go`, `plugins.go`, `init.go`, `init_ux.go`) end to end:
- `status.go` had real, previously-missed business logic -
  `internal/app/status` now holds `Compute`/`ExplainReason` (the
  build/evidence/publication/deployment state machine and its "Next/Why"
  reasoning), leaving `status.go` a thin flag-parse-and-format wrapper.
- `init_ux.go` had one small pure function worth moving -
  `filterResolvedUnknowns` → `internal/app/projectinit.FilterResolvedUnknowns`.
- `plugins.go` and `init.go` are **architecturally blocked**, not just
  low-value: both contain the sdk-to-domain translation boundary the
  no-sdk rule requires to live in cmd (`plugins.go` imports `sdk/plugin`
  directly; `init.go`'s three local helpers take `langplugin.Inspection`,
  an `sdk/langplugin` type, as a parameter). These can never move without
  either violating the dependency rule or redesigning the plugin
  capability-negotiation system itself.
- `plugin_provenance.go` and `init_legacydisk.go` are thin CLI/TTY
  wrappers over already-factored internal packages (`internal/provenance`,
  `internal/signing`, `internal/vmdisk`) with only a few-line pure sliver
  each - not worth fragmenting out.

Interface hardening: all 5 candidate `Service`-struct packages
converted; the `grep -rln "^type Service struct" internal/app/*/*.go`
check (§2 above) came back empty. There is no further work queued in
this document - every file in the original backlog has now been either
extracted or audited with a documented reason it wasn't. A future
session should treat this file as historical record, not an active
backlog, unless new `cmd/platform-factory` business logic or a new
`internal/app/*` `Service` struct appears and needs the same treatment.

### 2. Harden `internal/app/*` package boundaries with interfaces

Current shape: most `internal/app/*` packages export a concrete
`Service` struct with **exported, directly-mutable function fields**
(e.g. `provisionruntime.Service.PushOCI`), which cmd and tests overwrite
by field assignment. This works and is simple, but it means:
- cmd can construct a half-wired `Service{}` (zero-value function
  fields) and get a nil-pointer panic instead of a compile error or a
  clear runtime error.
- Nothing stops a caller from reaching past the intended API and
  swapping a field mid-use.

The harder Clean Architecture posture: each `internal/app/<domain>`
package should expose a narrow **interface** (the actual contract cmd
depends on) plus an unexported concrete implementation, e.g.:

```go
// internal/app/provisionruntime/provisionruntime.go
type Runtime interface {
    ResolveHostCandidate(language, targetArch string) string
    ProvisionFromRoot(loaded project.Loaded, language, imageRoot string, stderr io.Writer) (Manifest, error)
}

type service struct { /* unexported, same fields as today's Service */ }

func New() Runtime { return &service{ /* real wiring */ } }
```

Test/cmd overrides that need to swap one dependency (like
`LangpluginResolve`) move to an explicit functional option or a small
`NewForTest(...)`/`WithLangpluginResolve(...)` constructor, not direct
field mutation on an interface value (which is impossible by
construction - that's the point).

This is a real signature-and-call-site change across every
`internal/app/*` package that actually has a mutable `Service` struct,
plus every cmd file and test that constructs one, so it should be its
own careful, incremental pass - one package at a time, each gated the
same way (`go build`, `go vet`, `go test ./internal/archtest/... ./...`,
`GOOS=linux` cross-build) this session already established.

**Status: done for all 5 candidate packages** (`provisionruntime`,
`pipeline`, `sbom`, `verify`, `doctor`). A `grep -rln "^type Service
struct" internal/app/*/*.go` after Phases 4-11 turned up exactly these
5, not the originally-estimated 8 - `build`, `deploy`, `marketplace`,
`microvm`, `plugin`, `project`, `observe`, and `publish` are all
free-functions-shaped (no mutable struct to harden in the first place,
since this session's own extractions built them that way from the
start), and `migration` already used narrow interfaces before this
session. **Re-run that grep before assuming there's more work here** -
if it's empty, this goal is complete.

Per-package notes:
- **`provisionruntime`** (done first, the template - see above).
- **`pipeline`**: `Service struct{}` was already empty (no fields) -
  hardening it was almost a no-op for risk, done purely for
  consistency (`type Service interface { Decode; Plan; Run }` backed
  by unexported `service struct{}`).
- **`sbom`**, **`verify`**, **`doctor`**: unlike `provisionruntime`,
  these packages' own tests genuinely did mutate fields directly on a
  constructed value (`svc.Stat = ...`, `svc.VerifyLayout = ...`,
  `svc.LookPath = ...`, etc.) - the risk-free-conversion assumption
  from `provisionruntime` did **not** hold here. Fix: `fakeService()`
  test helpers now return the unexported `*service` concrete type
  instead of the `Service` interface, so same-package tests keep
  direct field-mutation access (Go's unexported-field visibility is
  package-scoped, not type-scoped) while every non-test caller only
  ever sees the interface. No `NewForTest`/functional-options
  constructor needed anywhere - same-package literal construction
  already covers every case that came up.
- Interface method sets were kept narrow where the extra methods were
  genuinely internal verification substeps (`verify`: only `Verify` is
  exported on the interface; `LoadTrustedKeys`/`VerifySignature`/
  `VerifyProvenance` stayed as methods on `*service`, still called
  directly by same-package tests) but kept complete where they're
  independent, cheap, plausibly-still-useful entry points (`doctor`:
  `Run`/`RunScope`/`RunScopeWithOptions` all kept, even though cmd only
  calls the last one).
- `sed -i '' -E` gotcha hit during `doctor`: BSD/macOS sed does not
  support `\b` or `\s` in either basic or extended mode the way GNU sed
  does - a first-pass rename silently matched nothing (or, worse,
  matched `os.UserHomeDir`/`os.ReadFile` as `s.UserHomeDir`/`s.ReadFile`
  substrings, corrupting real stdlib calls into `os.userHomeDir`/
  `os.readFile`). Fixed by using `[[:space:]]*` and anchoring on `^`
  instead of `\b`, and by eyeballing every sed-touched line afterward
  rather than trusting a clean exit code. Prefer targeted `Edit` calls
  over `sed` for this kind of rename when there's any doubt.

Suggested order: start with `internal/app/provisionruntime` (smallest,
already has the clearest two-method shape) as a template, confirm the
pattern reads well and doesn't fight the existing tests too hard, then
apply it to the rest.

**Done: `internal/app/provisionruntime`** (the template). Findings and
shape, for the remaining 7 packages:
- **The Service struct's injectable fields were never actually
  overridden by any test or caller anywhere in the repo** (grepped
  every `.Execute =`/`.PullImageRootfs =`/`.LookPath =`/
  `.OpenELFMachine =`/`.LangpluginResolve =` outside `New()`'s own
  wiring - only `LangpluginResolve` was ever set, and only once, in
  production). The "tests inject fakes" rationale in the old doc
  comment was aspirational, not load-bearing - this made the
  conversion strictly risk-free for this package. **Don't assume this
  holds for the other 7** - grep each one the same way before
  converting; a package whose tests genuinely do override fields needs
  real field-to-constructor-parameter test migration, not just a
  mechanical rename.
- Shape: `type Runtime interface { ResolveHostCandidate(...) string;
  PullImageRootfs(...) (string, error); ResolveLanguagePlugin(string)
  (string, error); ProvisionFromRoot(...) (Manifest, error) }`, backed
  by an unexported `*service` with unexported func-valued fields.
  `LangpluginResolve` (the one dependency `New()` used to leave unset)
  became a **required constructor parameter** - `New(resolveLangplugin
  func(string) (string, error)) Runtime` - turning "forgot to wire it"
  from a nil-func runtime panic into a compile error at the call site.
  Added `ResolveLanguagePlugin` to the interface (wrapping the same
  field) since `cmd`'s `autoProvisionRuntime` calls it directly, not
  just through `ProvisionFromRoot`.
- No `NewForTest`/functional-options constructor was added - nothing
  needs one yet (see the finding above). Add one only when a real test
  needs to fake a dependency; a speculative test-support constructor
  with zero callers is exactly the unneeded-abstraction this refactor
  argues against elsewhere.
- `Executor` (the exported func-type alias) and the `Service` struct
  itself had zero external references outside this package - both
  removed rather than kept for compatibility, confirmed via
  `grep -rln "provisionruntime\.(Executor|Service)"` across the repo
  first.
- Only one cmd call site needed changes:
  `cmd/platform-factory/provisionruntime.go`'s `newProvisionRuntimeService`
  (now returns `provisionruntime.Runtime`, calls `New(langplugin.Resolve)`
  directly instead of constructing then mutating a field) and
  `autoProvisionRuntime`'s `svc.LangpluginResolve(language)` →
  `svc.ResolveLanguagePlugin(language)`. `init.go`/`init_ux.go`/etc.
  never touched the type directly. Full gate green (build, vet,
  archtest, full suite, Linux cross-build) - zero test changes needed
  beyond the two `New()` call sites in
  `provisionruntime_test.go` gaining a `noLangpluginResolve` argument.

## Per-phase checklist (unchanged, still applies)

1. Read the target file(s) in full before moving anything.
2. Check whether any *other* file references the symbols being moved -
   `grep -rln '<symbol>' cmd/ internal/` before assuming a function is
   only used where it's defined. Twice this session a symbol turned out
   to be used from an unexpected third file (`WriteDockerArchive` from
   `main_test.go`, `validateBuildCapability` from `provisionruntime.go`).
3. Decide free-functions vs Service-struct (see the interface-hardening
   note above for the target shape going forward) based on whether the
   package has a real injectable-dependency surface.
4. Move tests: pure business-logic tests go with the code; CLI-adapter-
   level tests stay in `cmd/platform-factory`. When a test function
   mixes both (calls the CLI entry point AND asserts on an internal
   symbol), either split it or leave it in cmd with a qualified call -
   don't force a physical move that risks losing coverage.
5. Gate every phase:
   ```
   go build ./...
   go vet ./...
   go test ./internal/archtest/... ./...    # full repo; archtest is authoritative for the dependency rule
   GOOS=linux GOARCH=amd64 go build ./...
   ```
6. Report back before starting the next phase.

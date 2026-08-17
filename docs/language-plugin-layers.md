# Language plugins as separate modules — status

Two things this implements, per explicit direction: (1) language support
moves into `plugins/` as its own Go module/binary, matching
`plugins/containerd`/`plugins/kubevirt`'s existing pattern rather than
the lighter `sdk/plugin` subprocess-RPC protocol
(`examples/sdk/plugin-*`); (2) that plugin can ship its own pre-built
OCI layer, not just a file list the host assembles.

## Why this is a different mechanism from `sdk/plugin`

This repo already had a plugin mechanism for languages
(`sdk/plugin`, capabilities `detect`/`freeze`/`plan`, framed JSON-RPC
over stdin/stdout, working examples in Go/Python/JS/TS/C#) - but it's
strictly a *fallback*, consulted only when `cmd/platform-factory/project.go`'s
built-in language switch doesn't recognize the language at all, and it
has no way to contribute files to the image: `internal/project.ImageFiles()`
only ever reads `include`/`shared_deps` from `platform-factory.yaml`.

What's here is intentionally different and separate: a **plain binary**,
`exec`'d directly by name the same simple way `platform-factory-containerd`
(its config-rendering tool, not the containerd shim itself) already is - no
RPC framing, own `go.mod`, own release lifecycle. `platform-factory-kubevirt`
used to follow this same plain-exec shape but now speaks the `sdk/plugin`
RPC protocol instead, dispatched by capability through
`internal/plugin.Registry` (see `docs/containerd-kubernetes.md`'s Module
layout section) - it is no longer a fitting comparison here. Neither
language-plugin mechanism replaces the other; they now both exist for
different things.

The host never looks a language plugin up on bare `$PATH`. It resolves
`loaded.Config.Language` through `sdk/langplugin.Resolve`
(`cmd/platform-factory/language_plugin.go`), which only ever finds a
binary explicitly installed via `pf plugin load` - see "Managing
plugins: `pf plugin load/unload/list`" below. There is still no
in-process registry or load step inside the running `platform-factory`
process itself: `Resolve` re-reads the managed plugins directory on
every call, so `pf plugin load`/`unload` take effect immediately for
the next command, with no core restart - plugins are loaded and
unloaded on the fly by construction, not as a feature that had to be
built separately.

## What's implemented

- **Plugin-owned inspection**: `pf init` invokes
  `platform-factory-lang-<language> inspect --root DIR`. Each plugin owns its
  markers, source extensions, manifests, import analysis, runtime profile,
  build command, entrypoint and artifact inference. `cmd/` and `internal/`
  contain no fallback language detector or fixed language menu.
- **Host orchestration only**: the host runs the loaded plugins, preserves an
  ambiguous result, consumes the selected structured answer and adds only
  language-neutral operational hints. `sdk/langplugin.Inspect` supplies safe,
  deterministic filesystem mechanics, not a language catalogue.

- **`internal/oci.Options.ExtraLayers`** (`internal/oci/extralayers.go`):
  paths to pre-built, uncompressed tar files that become additional OCI
  manifest layers. Every entry is independently parsed and validated
  before Build trusts any of it - relative paths only (no absolute
  paths, no `..` traversal), only regular files and directories (no
  symlinks, hardlinks, devices, FIFOs), no setuid/setgid/sticky bits,
  bounded to 4 GiB / 200,000 entries per layer. Build re-hashes the
  actual bytes for both the layer digest and the `diff_id` - a plugin's
  own exit code or anything it prints is never trusted. 8 tests,
  including independent verification via `internal/layout.Verify` and a
  determinism check (same input tar → same manifest digest, twice).
- **`sdk/langplugin`** (`sdk/langplugin/langplugin.go`): the single
  shared implementation of `WriteDeterministicTar` - sorted entries,
  zeroed timestamps, symlinks rejected outright - that every
  `plugins/lang-<language>` module's `build-layer` subcommand calls.
  This used to be copy-pasted per language; it now lives in one place so
  the packaging rules (what a plugin-supplied layer may and may not
  contain) can't drift between languages. 3 tests, including a
  determinism check.
- **`plugins/lang-<language>`** for all 7 built-in languages: `python`,
  `node`, `ruby`, `php`, `java`, `dotnet`, `rust`. Each is its own
  `go.mod`, depending on the main module for exactly one thing -
  `sdk/langplugin` - via the same `require`+`replace ... => ../..`
  pattern `plugins/containerd` and `plugins/kubevirt` already use for
  their own sdk dependencies (`sdk/microvm`, etc.). The dependency only
  ever flows one way: a language plugin imports `sdk/langplugin` (a
  public, versioned API surface, not an internal package); nothing in
  the main module ever imports from `plugins/*`, and no plugin ever
  imports `sdk/plugin`, `internal/plugin`, or anything else internal to
  the main module. `main.go` lives at the module root, no
  `cmd/<binary-name>/` subdirectory. Each implements the same two
  subcommands:
  - `platform-factory-lang-<language> freeze --root DIR` - for
    python/node/ruby/php, this reproduces the host's existing built-in
    freeze step byte-for-byte (same commands, same install-location
    convention: pip's `--target`, npm's `node_modules`, Bundler's
    `vendor/cache`, Composer's `vendor` - each already project-local by
    default, no redirection needed). For java/dotnet/rust, freeze
    **deliberately deviates** from the built-in command: Maven/Gradle,
    NuGet, and Cargo all default to a shared, unbounded, per-user global
    cache (`~/.m2`/`~/.gradle`, `~/.nuget/packages`, `~/.cargo/registry`)
    that can't be packaged into a layer as-is, so each redirects into a
    project-local directory instead (`-Dmaven.repo.local=`/
    `GRADLE_USER_HOME=`, `dotnet restore --packages`, `CARGO_HOME=`),
    landing under `.platform-factory/deps/<language>/...`. Java also
    detects which build tool is present (`mvnw` → `gradlew` → bare
    `pom.xml`) in the same priority order the built-in adapter uses.
  - `platform-factory-lang-<language> build-layer --root DIR --output
    TAR --dest PREFIX` - calls `sdk/langplugin.WriteDeterministicTar` on
    whatever freeze installed.

  Each module has 4-10 tests (Java's build-tool detection needs more
  cases than the others); the tar-writer's own behavior (sorting, zeroed
  timestamps, symlink rejection, determinism) is covered once in
  `sdk/langplugin`'s own tests rather than per language. Verified
  independently per module via `GOWORK=off go build ./... && go vet
  ./... && go test ./...` and `gofmt -l .`.
- **Host dispatch** (`cmd/platform-factory/language_plugin.go`): a new
  `platform-factory.yaml` field, `language_plugin: true`, opts a project
  in. `buildProject` then calls `platform-factory-lang-<language>
  build-layer` and adds the result to `oci.Options.ExtraLayers`. **Off
  by default** - every project that doesn't set this field is
  byte-for-byte unaffected; there is no "binary happens to be on PATH"
  auto-detection, on purpose (explicit opt-in, not spooky action at a
  distance). 5 tests using a mocked executor (no real binary needed to
  exercise the dispatch logic).

## Managing plugins: `pf plugin load/unload/list`

A beginner never needs to know a language plugin is a binary, that it's
resolved through a directory, or what that directory is called - three
verbs are the entire surface:

```sh
pf plugin load python                        # turns python on
pf plugin load --from ./my-plugin acme       # loads a plugin binary of your own
pf plugin load --from ./my-plugin-src acme   # builds a plugin from its Go module source, then loads it
pf plugin list                                # what's on, what isn't
pf plugin unload python                       # turns it back off
```

`--from` also accepts plugins implemented without Go:

```sh
pf plugin load --from ./plugin.py acme-python
pf plugin load --from ./plugin.js acme-js
pf plugin load --from ./plugin.ts acme-typescript
pf plugin load --from ./plugin.php acme-php
pf plugin load --from ./Plugin.cs acme-csharp
pf plugin load --from ./Plugin.csproj acme-csharp
pf plugin unload acme-python
```

A loaded language plugin can scaffold another plugin in its own language; PF
only delegates and owns no language template:

```sh
pf plugin create --language python --output ./acme-python acme
pf plugin create --language node --dialect js --output ./acme-js acme-js
pf plugin create --language node --dialect ts --output ./acme-ts acme-ts
pf plugin create --language php --output ./acme-php acme-php
pf plugin create --language dotnet --output ./acme-csharp acme-csharp
```

The generated source is deliberately minimal and immediately loadable. The
author edits its `inspect` answer to add markers, build inference or other
language-specific behavior, then uses the normal `pf plugin load --from` flow.

Python requires `python3`, JavaScript/TypeScript requires Node, PHP requires
`php`, and C# requires the .NET SDK at load time. TypeScript is transformed to
JavaScript before installation. C# is published as a self-contained
single-file executable. The managed registry therefore contains a stable
executable rather than a reference to the original source path.

Every load is immediately probed with `inspect --root <empty-directory>`.
Invalid JSON or a non-zero exit triggers automatic unload. Plugin names accept
only lowercase letters, digits, `.`, `_` and `-`, preventing registry path
traversal.

Implementation, all in `sdk/langplugin/registry.go` (the same public
package a third-party plugin author would use - the CLI in
`cmd/platform-factory/plugin.go` has no special access the SDK
doesn't):

- **`Dir()`** - `~/.platform-factory/plugins` by default, overridable
  via `PLATFORM_FACTORY_LANG_PLUGIN_DIR` (tests use this to stay
  hermetic; nothing else needs to).
- **`Resolve(name)`** - the absolute path to `name`'s loaded binary, or
  an error that already names the fix: `` language plugin "python"
  isn't loaded - run `pf plugin load python` first ``. This is what
  `language_plugin.go`'s dispatch calls instead of an `exec.Command`
  bare-name lookup - "loaded" has exactly one meaning, presence in this
  one directory, never ambient `$PATH`.
- **`Load(name, sourcePath)`** - copies `sourcePath` into `Dir()` as
  `name`'s plugin (atomically: written to a temp file, then renamed
  into place, so a concurrent `Resolve` never observes a half-written
  binary). Loading over an already-loaded name replaces it, rather than
  erroring - re-running `pf plugin load python` after rebuilding the
  binary is the expected way to pick up a new version. `Load` itself
  always takes a path to an already-built regular file; the CLI's
  `prepareSource` (`cmd/platform-factory/plugin.go`, not part of the SDK) is
  what prepares Go, Python, JavaScript, TypeScript, PHP and C# sources before
  the atomic SDK installation.
- **`Unload(name)`** - removes it. Unloading something never loaded is
  a no-op, not an error: the end state the caller asked for (not
  loaded) already holds either way.
- **`List()`** - every currently-loaded name, sorted.

`cmd/platform-factory/plugin.go`'s `pf plugin load NAME` (no `--from`)
only works for platform-factory's own 7 built-in languages -
`locateBuiltinPluginBinary` looks next to the running `platform-factory`
binary first (where a bundled install would put it), then on `$PATH`
(for a from-source build where each `plugins/lang-*` module was built
separately), and fails with a command to build it if neither is found.
Any other name requires `--from PATH` naming the binary explicitly - a
deliberate two-tier design: zero-knowledge for the 7 languages
platform-factory ships, one explicit flag for anything else, and
`--from` itself accepts a binary or a supported source file/directory. The
tests cover actual load, inspection and unload for Python, JavaScript,
TypeScript, PHP and C#, plus invalid-contract rollback and registry safety.

## Scope: language modules

Go now also has a `plugins/lang-go` module for inspection. `custom` remains a
user-defined contract and therefore has no language-specific detector.

## Verification status

Fully built, vetted, and tested, both per-module and as a whole
workspace:
```sh
go build ./... && go vet ./... && go test ./...          # from repo root, whole go.work
```
plus, for each `plugins/lang-*` module individually (each resolves its one
dependency, `sdk/langplugin`, via its own `replace` directive, so this also
proves it's unaffected by anything else in the tree):
```sh
cd plugins/lang-<language> && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./... && gofmt -l .
```
`cmd/platform-factory`'s host-dispatch files (`language_plugin.go`,
`language_plugin_test.go`, the `buildProject` wiring in `project.go`)
compile and pass along with the rest of the package - the earlier
`internal/guesttransport` build blocker that prevented compiling
`cmd/platform-factory` at all has since been resolved.

## Suggested next slice

- Decide whether `pf project freeze` should also delegate to
  `platform-factory-lang-<language> freeze` when `language_plugin` is
  set - today freeze still uses the built-in adapter even for opted-in
  projects; only the *build* step's extra layer is plugin-sourced. This
  is still an open design question, not yet resolved either way.
- The installer (`cmd/platform-factory-installer`) doesn't yet build or
  bundle `plugins/lang-*` binaries, so a fresh install has nothing for
  `pf plugin load <language>` to find next to the `platform-factory`
  binary until a user builds one from source. Teaching the installer to
  build and place them (as an optional component, matching the
  existing `builder`/`microvm`/`distributed` components in
  `components.go`) would close that gap and make `pf plugin load`
  work out of the box for a fresh install, not just a from-source
  checkout.

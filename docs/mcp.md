# MCP server

`platform-factory mcp serve` runs a native [Model Context Protocol](https://modelcontextprotocol.io)
server over stdio, giving an MCP-speaking client (Claude Code, Codex, Cursor,
ChatGPT, ...) a purpose-built interface onto this repository: inspecting its
architecture, listing and validating plugins, driving the real
`init`/`build`/`publish`/`deploy`/`status`/`doctor`/`detect`/`verify`/`inspect`
product commands, and proposing changes - core changes, new/modified plugins,
or both - as a reviewable draft pull request, with a comprehensive PR
description when the change fixes a bug. It is implemented entirely in
`internal/mcp/...` with no external MCP SDK dependency (the JSON-RPC 2.0
stdio transport is hand-rolled, matching this repository's general
preference for minimal external dependencies), and it reuses the project's
existing plugin manifests, git conventions, and CI checks rather than
inventing parallel ones.

## Installing and starting it

The server is a subcommand of the `platform-factory` binary already built by
this repository - there is nothing separate to install.

```
go build -o platform-factory ./cmd/platform-factory
./platform-factory mcp serve --repo /path/to/platform-factory
```

`--repo` defaults to the current directory. The process refuses to start if
that directory has no `go.mod` at its root.

stdout carries only JSON-RPC protocol messages; every diagnostic (including
the one-line "now serving" banner) goes to stderr, so a client piping stdout
never sees anything but the protocol.

### Client configuration

```json
{
  "mcpServers": {
    "platform-factory": {
      "command": "pf",
      "args": ["mcp", "serve"]
    }
  }
}
```

Set the client's working directory to the repository root, or pass
`--repo /path/to/platform-factory` explicitly.

### Running it as a container instead

`.github/workflows/ci-mcp-image.yml` publishes this server as a container
image to `ghcr.io/<owner>/<repo>-mcp`, tagged `:latest` on every push to
`main` and `:vX.Y.Z` on version tags. The image content is produced
entirely by this repository's own native OCI builder
(`cmd/oci-builder`, `scripts/ci/build-mcp-image-layout.sh`) - no
Dockerfile `RUN` step compiles anything; the repository-root `Dockerfile`
only re-wraps the already-built, already-verified layout so `docker
buildx` can push a real multi-arch manifest list, the same split already
used for the main release image in `ci-release.yml`.

The image's `ENTRYPOINT` is the `platform-factory` binary itself, with no
baked-in default arguments - the underlying `FROM scratch` build has no
shell to compute a dynamic default `CMD` at container-start time, so the
full `mcp serve --repo /workspace` invocation is the client's job to
supply, the same way any other MCP server distributed as a container
image works:

```json
{
  "mcpServers": {
    "platform-factory": {
      "command": "docker",
      "args": [
        "run", "--rm", "-i",
        "-v", "/path/to/platform-factory:/workspace",
        "ghcr.io/cypt71/platform-factory-base-mcp:latest",
        "mcp", "serve", "--repo", "/workspace"
      ]
    }
  }
}
```

## Two tiers of mutating tools

Every tool that only reads (`pf_project_inspect`, `pf_plugin_list`,
`pf_core_inspect`, ...) works with zero configuration.

Tools that change something come in two forms:

- **Client-orchestrated primitives** - always available, take structured
  parameters, and do exactly what they're told: `pf_plugin_create`,
  `pf_core_read_file`, `pf_core_write_file`, `pf_core_self_check`,
  `pf_git_status`, `pf_git_prepare_branch`, `pf_git_commit`,
  `pf_core_create_pr`. An MCP client's own model drives these across
  multiple tool calls, the way MCP tools normally work.
- **Server-embedded agent mode** - opt-in, and built *on top of* the same
  primitives above (in-process function calls, not a self-referential MCP
  round-trip): `pf_plugin_modify`, `pf_core_patch`, and `pf_implement` accept
  a free-text `request` and have the server call its own configured LLM to
  turn that into concrete edits. This tier requires
  `PLATFORM_FACTORY_MCP_ANTHROPIC_API_KEY` (an Anthropic API key) in the
  server process's environment. Without it, these three tools return a
  structured `agent_unavailable` error naming the primitives to use instead
  - never a silent no-op, never a fabricated result.

Optionally, `PLATFORM_FACTORY_MCP_ANTHROPIC_MODEL` overrides the model
(default `claude-sonnet-4-5`).

Every agent-mode loop is bounded to 3 attempts (plan → edit → validate,
retrying with the validation failure fed back to the model) before giving up
with a clear error.

A third group, **product tools**, is not about this repository's own source
at all: `pf_init`, `pf_build`, `pf_publish`, `pf_deploy`, `pf_project`,
`pf_status`, `pf_doctor`, `pf_detect`, `pf_verify`, `pf_inspect` re-exec the
running `platform-factory` binary itself with a fixed subcommand and a
structured, schema-derived argument list (never a shell string, never a
caller-chosen subcommand) - the same commands you'd run by hand at a
terminal, exposed as MCP tools so a client can
`init`/`build`/`publish`/`deploy`/`run` a project the same way the CLI does.
Each accepts an `extra_args` array for flags the schema doesn't model by
name; it is still one argument per array element handed straight to
`exec.Command`, never shell-interpreted.

`pf_build`/`pf_publish`/`pf_deploy` operate on a standalone executable or an
already-built OCI layout. `pf_project` is the other half: it drives a
`pf.yaml`-based project's full lifecycle
(`show`/`plan`/`freeze`/`build`/`run`/`launch`/`migrate`) and is the only
tool that can actually **run** a built image - `run`/`launch` shell to the
host's `docker`/`podman` (or a native microVM runtime), so they require that
runtime to be reachable from wherever the MCP server process itself runs.
If the server is running inside a container (see "Running it as a container
instead" below), it needs the container runtime available *inside* that
container too (a mounted socket and CLI) - the plain `FROM scratch` image
this repository publishes has neither, by design, so `pf_project`'s
`run`/`launch` actions only work when the server runs natively on the host.

### Pinning PRs to a specific GitHub repository

`pf_core_create_pr` and `pf_implement` open their PR against whatever `gh`
would infer from the working tree's own remotes, unless overridden. Set
`PLATFORM_FACTORY_MCP_TARGET_REPO` (owner/repo, e.g.
`CYPT71/platform-factory`) in the server's environment to pin every PR this
server opens to that exact repository regardless of `--repo`'s working tree
- useful when the server is driven against a local checkout that isn't
itself the canonical upstream. `pf_core_create_pr` also accepts a per-call
`repo` argument that takes precedence over the env var for a single PR.

## Tools

| Tool | Tier | Description |
| --- | --- | --- |
| `pf_project_inspect` | read | Module, version, git branch/dirty state, validation commands, and (at `depth: detailed`) top-level components. |
| `pf_plugin_list` | read | Every `plugins/<name>` directory, classified `rpc` (has `plugin.json`) or `language-command`. |
| `pf_plugin_inspect` | read | One plugin's manifest, module kind, and test files. |
| `pf_plugin_create` | primitive | Scaffold a new RPC plugin: `plugin.json`, README, go.mod, and a `cmd/platform-factory-<name>/main.go` with a real `sdk/plugin.Server` and one handler stub per requested capability. |
| `pf_plugin_validate` | read | Manifest schema, executable digest, a real `go build` of the plugin's own module, language-family permission rules, and test-file presence. |
| `pf_plugin_modify` | agent | Modify an existing plugin from a free-text request, confined to that plugin's own directory. |
| `pf_core_inspect` | read | One architectural area's `internal/` packages (doc comments, source/test files), or all of them. Areas: `marketplace`, `runtime`, `builder`, `registry`, `supply-chain`, `microvm`, `cli`, `all`. |
| `pf_core_validate` | read | Run profile `fast` (gofmt+vet), `full` (+ build/test/archtest), `security` (local mirrors of `ci-security.yml`'s static checks + govulncheck), or `affected` (tests scoped to the real reverse-dependency closure of the currently changed files). |
| `pf_core_read_file` | primitive | Read one repo-relative file, confined to the repository root. |
| `pf_core_write_file` | primitive | Write one repo-relative file, confined to the repository root; refuses to write through a symlink. |
| `pf_core_self_check` | primitive | Run `go test ./internal/archtest/...` against the working tree. |
| `pf_core_patch` | agent | Propose a core change from a free-text request, confined to a caller-supplied `allowed_paths` list. |
| `pf_git_status` | primitive | Current branch and dirty state. |
| `pf_git_prepare_branch` | primitive | Create and check out a new branch from a clean HEAD. Refuses `main`/`master`, an unsafe name, an existing name, or a dirty tree. |
| `pf_git_commit` | primitive | Stage exactly the given paths and commit. Refuses to commit directly to `main`/`master`. |
| `pf_core_create_pr` | primitive | Push the current branch and open a **draft** PR via `gh`, optionally targeting a `repo` override. No merge tool exists anywhere in this server. |
| `pf_implement` | agent | Classify a request as plugin-only / core-only / both; branch; implement it; and, on success, commit, push, and open a draft PR with a comprehensive description - end to end in one call, whether the change is plugin-only, core-only, or both. |
| `pf_init` | product | `platform-factory init`: scaffold a `pf.yaml` project from a source directory. |
| `pf_build` | product | `platform-factory build`: build the nearest `pf.yaml` project, or wrap one compiled executable directly. |
| `pf_publish` | product | `platform-factory publish`: push a verified OCI layout to a registry, optionally signing/attesting it. Credentials come from the server's own environment, never a tool argument. |
| `pf_deploy` | product | `platform-factory deploy`: apply a digest-pinned image to Kubernetes. Secret values are never accepted directly, only `ENV=SECRET/KEY` references. |
| `pf_project` | product | `platform-factory project <action>`: a `pf.yaml` project's full lifecycle - `show`/`plan`/`freeze`/`build`/`run`/`launch`/`migrate`. `run`/`launch` are the only tools in this server that actually start a built image. |
| `pf_status` | product | `platform-factory status`: build/publish/deploy state and the next safe command. |
| `pf_doctor` | product | `platform-factory doctor`: check local tools, runtimes, and hardware support. |
| `pf_detect` | product | `platform-factory detect`: classify an application input without executing it. |
| `pf_verify` | product | `platform-factory verify`: strictly validate a local OCI layout. |
| `pf_inspect` | product | `platform-factory inspect`: summarize a local OCI layout's manifests and platforms. |

## Resources

| URI | Description |
| --- | --- |
| `pf://project` | Detailed project snapshot (same shape as `pf_project_inspect` at `depth: detailed`). |
| `pf://architecture` | Every `internal/` package's own doc comment, plus the CLI's top-level commands - read live from source, not hand-written prose. |
| `pf://plugins` | Every `plugins/<name>` directory with kind/family/capabilities where known. |
| `pf://plugins/schema` | The `plugin.json` manifest schema, as enforced by `internal/plugin.Manifest.Validate()`. |
| `pf://marketplace` | This machine's locally tracked marketplace sources and synced index (offline, no network call). |
| `pf://core` | Every `pf_core_inspect` area and the packages it maps to, plus the compatibility rules those packages must respect. |
| `pf://core/packages` | Every `internal/` package with its doc comment and file listing. |

## Plugin workflow

1. `pf_plugin_list` / `pf_plugin_inspect` to see what already exists.
2. `pf_plugin_create` (or `pf_plugin_modify` in agent mode) to scaffold or
   change one.
3. `pf_plugin_validate` to check the manifest, build, and digest.
4. Once you've built the real executable and updated its digest,
   `platform-factory plugin install --from plugins/<name>` to install it
   locally.

### Example: adding Bun support

```
pf_implement {"request": "Add support for Bun applications"}
```

`pf_implement` inspects existing capabilities, decides Bun can be supported
entirely as a new plugin (no core API is missing), and calls the same code
path as:

```
pf_plugin_create {
  "name": "bun-builder",
  "description": "Support Bun applications",
  "capabilities": ["detect", "build"]
}
```

which writes `plugins/bun-builder/{plugin.json,README.md,go.mod,cmd/platform-factory-bun-builder/main.go}`.
Since nothing in `internal/` needed to change, `pf_implement`'s result has a
`plugin` field and no `core` field - but a successful plugin-only change
still opens a draft PR the same way a core change does, so `pull_request`
is set here too.

### Example: proposing a bug fix

```
pf_implement {"request": "detect misclassifies a Bun app with a package.json as Node - fix the language detector to check for bun.lockb first"}
```

When the model's classification includes a bug report, it also returns
`root_cause`/`fix` fields alongside the usual plugin/core plan; `pf_implement`
carries them into the PR body as dedicated **Bug: Root Cause** and **Fix**
sections, in addition to the file list and validation results every PR body
already includes - so the PR explains itself, not just what changed.

## Core-change workflow

1. `pf_core_inspect` to find the relevant packages, or `pf_core_read_file` to
   read specific ones directly.
2. `pf_git_prepare_branch` (recommended name: `mcp/<type>/<slug>`, e.g.
   `mcp/feat/plugin-detector-api`) - branch from a clean tree *before*
   editing, since `pf_git_prepare_branch` refuses to run against a dirty
   working tree.
3. `pf_core_write_file` (or `pf_core_patch` in agent mode, given an explicit
   `allowed_paths`) to make the change.
4. `pf_core_self_check`, then `pf_core_validate {"profile": "affected"}` (or
   `"full"` before calling it done).
5. `pf_git_commit`, `pf_core_create_pr`.

`pf_implement` runs this same branch-first-then-edit order itself (steps
2-5, plus the equivalent plugin steps when relevant), using the same
underlying functions - it creates the branch before calling
`pf_plugin_modify`/`pf_core_patch`'s underlying logic, precisely so the
working tree is still clean when it does.

## Security

- **Path confinement**: every read/write primitive resolves its path,
  rejects `..` segments and absolute paths, and re-resolves the path through
  any existing symlinked ancestor directories - a write can never land, and a
  read can never pull from, outside the repository root. See
  `internal/mcp/core/patch.go`'s `resolveScopedPath` and its test suite
  (`internal/mcp/core/patch_test.go`), which specifically covers symlinked
  parent directories and symlinked write targets, not just plain `..`
  traversal.
- **No arbitrary shell**: `internal/mcp/git` never builds a shell command
  string; every git/`gh` invocation is `exec.Command` with a typed,
  argument-array API (branch name, commit message, PR title/body) - there is
  no "run this git command" tool anywhere in the registry.
- **`main`/`master` protection**: `pf_git_prepare_branch` refuses to branch
  from a dirty tree or to create a protected-named branch;
  `pf_git_commit`/`pf_core_create_pr`'s underlying `Push` refuse to act
  directly on `main`/`master`. There is no merge tool in this server at all
  - not a merge tool that always errors, genuinely absent from the tool
  registry.
- **Credential handling**: the GitHub token is never read or held by this
  process - `pf_core_create_pr` delegates entirely to the already-
  authenticated `gh` CLI, including for the target repository itself:
  `PLATFORM_FACTORY_MCP_TARGET_REPO`/the per-call `repo` argument only ever
  become `gh`'s own `--repo owner/repo` flag, never a URL this process
  parses or connects to itself. The Anthropic API key is read once from
  `PLATFORM_FACTORY_MCP_ANTHROPIC_API_KEY`, never logged, and never echoed
  in a tool result or PR body.
- **Bounded agent loops**: `pf_plugin_modify`/`pf_core_patch`/`pf_implement`
  retry at most 3 times and are confined to the plugin's own directory or an
  explicit `allowed_paths` list respectively - never an open-ended write
  scope.
- **Strict input decoding**: every tool argument is JSON-decoded into a
  specific Go struct; malformed or unexpected input fails the call with a
  structured error rather than partially applying.

## Limitations

- `pf_core_validate {"profile": "security"}`'s `govulncheck` step is
  `skipped` (not failed) when the `govulncheck` binary isn't installed - it
  is not vendored or auto-installed by this server.
- The `affected` validation profile computes the reverse-dependency closure
  from `go list -json ./...`, which only sees the current Go workspace's
  modules; a change whose only effect is on a plugin module not `use`d in
  `go.work` won't be picked up by it (run `pf_plugin_validate` for that
  plugin directly instead).
- `pf_plugin_modify`/`pf_core_patch`/`pf_implement`'s classification and
  generated edits are only as good as the underlying model's reply; the
  bounded retry loop catches a reply that fails validation, not one that
  passes validation while being a poor design choice - human review of the
  resulting draft PR remains the actual gate before anything merges.

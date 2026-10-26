# Claude Code / ECC hook: freeze guard

A Claude Code hook that keeps `.platform-factory/freeze.lock.json` in sync with
your dependency manifests while an agent edits a Platform Factory project.

| Event | Trigger | Behavior |
|-------|---------|----------|
| `PostToolUse` | `Edit`/`Write`/`MultiEdit` on `package-lock.json`, `go.sum`, `requirements.txt`, `Cargo.lock`, `pom.xml`, … | Tells the agent to run `pf project freeze`. |
| `PreToolUse` | `Bash` running `pf build\|publish\|launch\|deploy` | Warns when a manifest is newer than the freeze lock; denies with `PF_GUARD_STRICT=1`. |

The hook never runs `pf` or project code (consistent with the
[AI hooks policy](../../docs/ai-hooks-and-autofix.md): a cloned repository must
not activate executables on its own). It is a no-op outside a Platform Factory
project (a `pf.yaml`-style config from `pf init`, or a `.platform-factory/`
directory), and it fails open on any internal error.

| Variable | Effect |
|----------|--------|
| `PF_GUARD=off` | Disable the hook. |
| `PF_GUARD_STRICT=1` | Deny instead of warn on a stale freeze before a build. |

## Install

Requires Node.js 18+. This directory is a Claude Code plugin
(`platform-factory-guard`), so `${CLAUDE_PLUGIN_ROOT}` in `hooks/hooks.json`
resolves to it automatically.

```text
/plugin marketplace add CYPT71/platform-factory
/plugin install platform-factory-guard@platform-factory
```

To try it from a local checkout without installing:
`claude --plugin-dir integrations/claude-code`.

## Using it with ECC

The plugin is independent of [ECC](https://github.com/affaan-m/everything-claude-code)
and runs next to ECC's own hooks: both are plain `PreToolUse`/`PostToolUse`
command hooks, and Claude Code runs all matching hooks. ECC ships a
`platform-factory` skill that documents the full `pf` workflow and this hook.

## Test

```bash
node --test integrations/claude-code/tests/pf-freeze-guard.test.js
```

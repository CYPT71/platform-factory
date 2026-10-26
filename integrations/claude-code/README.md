# Claude Code / ECC hook: freeze guard

A Claude Code hook that keeps `.platform-factory/freeze.lock.json` in sync with
your dependency manifests while an agent edits a Platform Factory project.

| Event | Trigger | Behavior |
|-------|---------|----------|
| `PostToolUse` | `Edit`/`Write`/`MultiEdit` on `package-lock.json`, `go.sum`, `requirements.txt`, `Cargo.lock`, `pom.xml`, … | Tells the agent to run `pf freeze`. |
| `PreToolUse` | `Bash` running `pf build\|publish\|launch\|deploy` | Warns when a manifest is newer than the freeze lock; denies with `PF_GUARD_STRICT=1`. |

The hook never runs `pf` or project code (consistent with the
[AI hooks policy](../../docs/ai-hooks-and-autofix.md): a cloned repository must
not activate executables on its own). It is a no-op outside a project that has a
`.platform-factory/` directory, and it fails open on any internal error.

| Variable | Effect |
|----------|--------|
| `PF_GUARD=off` | Disable the hook. |
| `PF_GUARD_STRICT=1` | Deny instead of warn on a stale freeze before a build. |

## Install

Requires Node.js 18+. Pick one:

**Standalone** — copy `scripts/pf-freeze-guard.js` somewhere stable and merge
`hooks/hooks.json` into `~/.claude/settings.json` (or the project's
`.claude/settings.json`), replacing `${CLAUDE_PLUGIN_ROOT}` with that directory.

**With [ECC](https://github.com/affaan-m/everything-claude-code)** — install ECC,
then add the same two entries to your ECC-managed hooks. The matching ECC skill
(`platform-factory`) documents the full workflow and is loaded automatically when
a project contains `.platform-factory/`.

## Test

```bash
node --test integrations/claude-code/tests/pf-freeze-guard.test.js
```

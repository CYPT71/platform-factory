#!/usr/bin/env node
'use strict';

/**
 * Platform Factory freeze guard — Claude Code hook (PreToolUse + PostToolUse).
 *
 * Purpose: keep `.platform-factory/freeze.lock.json` in sync with the
 * dependency manifests of a Platform Factory project while Claude Code edits it.
 *
 *   PostToolUse (Edit|Write|MultiEdit): a dependency manifest was changed ->
 *     remind Claude to run `pf freeze`.
 *   PreToolUse  (Bash): `pf build|publish|launch|deploy` is about to run while
 *     a manifest is newer than the freeze lock -> warn (or deny in strict mode).
 *
 * The hook NEVER executes `pf` or any project code. It only reads file
 * metadata and prints a JSON decision on stdout. It is a no-op outside a
 * Platform Factory project (no `.platform-factory/` directory).
 *
 * Environment:
 *   PF_GUARD=off      disable the hook entirely
 *   PF_GUARD_STRICT=1 deny (instead of warn) on a stale freeze before build
 *
 * Fail-open: any internal error exits 0 so a bug here never blocks a session.
 */

const fs = require('node:fs');
const path = require('node:path');

const LOCK_REL = path.join('.platform-factory', 'freeze.lock.json');

// Manifests/lockfiles that feed `pf freeze` (see docs/cli-guide.md).
const MANIFESTS = new Set([
  'package.json', 'package-lock.json', 'npm-shrinkwrap.json', 'yarn.lock', 'pnpm-lock.yaml',
  'requirements.txt', 'requirements.lock', 'poetry.lock', 'Pipfile.lock', 'uv.lock',
  'go.mod', 'go.sum', 'Cargo.toml', 'Cargo.lock',
  'pom.xml', 'build.gradle', 'build.gradle.kts', 'Gemfile', 'Gemfile.lock',
  'composer.json', 'composer.lock', 'packages.lock.json',
]);

const BUILD_VERBS = new Set(['build', 'publish', 'launch', 'deploy']);

function findProjectRoot(start) {
  let dir = path.resolve(start || process.cwd());
  for (;;) {
    if (fs.existsSync(path.join(dir, '.platform-factory'))) return dir;
    const parent = path.dirname(dir);
    if (parent === dir) return null;
    dir = parent;
  }
}

function mtime(file) {
  try { return fs.statSync(file).mtimeMs; } catch { return null; }
}

function isManifest(file) {
  return typeof file === 'string' && MANIFESTS.has(path.basename(file));
}

/** True when the Bash command invokes pf/platform-factory with a build verb. */
function isPfBuild(command) {
  if (typeof command !== 'string') return false;
  return command.split(/&&|\|\||;|\|/).some((segment) => {
    const tokens = segment.trim().split(/\s+/);
    const bin = path.basename(tokens[0] || '').replace(/\.exe$/, '');
    if (bin !== 'pf' && bin !== 'platform-factory') return false;
    const verb = tokens.slice(1).find((t) => !t.startsWith('-'));
    return BUILD_VERBS.has(verb) || (verb === 'project' && BUILD_VERBS.has(tokens[tokens.indexOf('project') + 1]));
  });
}

/** Manifests in the project root newer than the freeze lock. */
function staleManifests(root, lockTime) {
  let entries;
  try { entries = fs.readdirSync(root); } catch { return []; }
  return entries.filter((name) => {
    if (!MANIFESTS.has(name)) return false;
    const t = mtime(path.join(root, name));
    return t !== null && t > lockTime;
  });
}

function evaluate(input, env = process.env) {
  if (env.PF_GUARD === 'off' || !input || typeof input !== 'object') return null;
  const root = findProjectRoot(input.cwd);
  if (!root) return null;
  const tool = input.tool_name;
  const args = input.tool_input || {};

  if (tool === 'Edit' || tool === 'Write' || tool === 'MultiEdit') {
    if (!isManifest(args.file_path)) return null;
    return {
      event: 'PostToolUse',
      context: `[pf-freeze-guard] ${path.basename(args.file_path)} changed: run \`pf freeze\` to refresh ${LOCK_REL} before \`pf build\`.`,
    };
  }

  if (tool === 'Bash' && isPfBuild(args.command)) {
    const lockTime = mtime(path.join(root, LOCK_REL));
    if (lockTime === null) return null; // never frozen: pf itself reports it
    const stale = staleManifests(root, lockTime);
    if (stale.length === 0) return null;
    const message = `[pf-freeze-guard] ${stale.join(', ')} newer than ${LOCK_REL}: run \`pf freeze\` first.`;
    return env.PF_GUARD_STRICT === '1'
      ? { event: 'PreToolUse', deny: message }
      : { event: 'PreToolUse', context: message };
  }
  return null;
}

function render(result) {
  if (!result) return '';
  const out = { hookSpecificOutput: { hookEventName: result.event } };
  if (result.deny) {
    out.hookSpecificOutput.permissionDecision = 'deny';
    out.hookSpecificOutput.permissionDecisionReason = result.deny;
  } else {
    out.hookSpecificOutput.additionalContext = result.context;
  }
  return JSON.stringify(out);
}

function main() {
  let raw = '';
  process.stdin.setEncoding('utf8');
  process.stdin.on('data', (c) => { raw += c; });
  process.stdin.on('end', () => {
    try {
      const out = render(evaluate(JSON.parse(raw || '{}')));
      if (out) process.stdout.write(out);
    } catch { /* fail-open */ }
    process.exit(0);
  });
}

if (require.main === module) main();

module.exports = { evaluate, render, isPfBuild, isManifest, findProjectRoot };

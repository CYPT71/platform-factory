'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { evaluate, render, isPfBuild, findProjectRoot } = require('../scripts/pf-freeze-guard.js');

const SCRIPT = path.join(__dirname, '..', 'scripts', 'pf-freeze-guard.js');

function project({ frozen = true, manifestNewer = false } = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pf-guard-'));
  fs.mkdirSync(path.join(root, '.platform-factory'));
  const lock = path.join(root, '.platform-factory', 'freeze.lock.json');
  const manifest = path.join(root, 'package-lock.json');
  fs.writeFileSync(manifest, '{}');
  if (frozen) fs.writeFileSync(lock, '{}');
  const old = new Date(Date.now() - 60_000);
  const recent = new Date();
  if (frozen) fs.utimesSync(lock, manifestNewer ? old : recent, manifestNewer ? old : recent);
  fs.utimesSync(manifest, manifestNewer ? recent : old, manifestNewer ? recent : old);
  return { root, manifest };
}

test('isPfBuild matches pf build verbs only', () => {
  assert.equal(isPfBuild('pf build .'), true);
  assert.equal(isPfBuild('cd x && ./bin/platform-factory launch --publish'), true);
  assert.equal(isPfBuild('pf project build --dry-run'), true);
  assert.equal(isPfBuild('pf detect ./app'), false);
  assert.equal(isPfBuild('echo pf build'), false);
  assert.equal(isPfBuild(undefined), false);
});

test('no-op outside a Platform Factory project', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'pf-none-'));
  assert.equal(evaluate({ cwd: dir, tool_name: 'Edit', tool_input: { file_path: 'package.json' } }), null);
});

test('post-edit of a manifest reminds to freeze', () => {
  const { root, manifest } = project();
  const r = evaluate({ cwd: root, tool_name: 'Write', tool_input: { file_path: manifest } });
  assert.match(r.context, /pf project freeze/);
  assert.equal(r.event, 'PostToolUse');
});

test('a freshly initialised project (pf.yaml, no .platform-factory yet) is recognised', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pf-init-'));
  fs.writeFileSync(path.join(root, 'pf.yaml'), 'version: 1\n');
  const manifest = path.join(root, 'package-lock.json');
  fs.writeFileSync(manifest, '{}');
  const r = evaluate({ cwd: root, tool_name: 'Edit', tool_input: { file_path: manifest } });
  assert.match(r.context, /pf project freeze/);
  // never frozen: a build is left to pf itself to report
  assert.equal(evaluate({ cwd: root, tool_name: 'Bash', tool_input: { command: 'pf build' } }), null);
});

test('messages use forward slashes on every OS', () => {
  const { root, manifest } = project();
  assert.match(evaluate({ cwd: root, tool_name: 'Edit', tool_input: { file_path: manifest } }).context, /\.platform-factory\/freeze\.lock\.json/);
});

test('post-edit of a non-manifest is silent', () => {
  const { root } = project();
  assert.equal(evaluate({ cwd: root, tool_name: 'Edit', tool_input: { file_path: path.join(root, 'main.go') } }), null);
});

test('pf build with a stale lock warns, or denies in strict mode', () => {
  const { root } = project({ manifestNewer: true });
  const input = { cwd: root, tool_name: 'Bash', tool_input: { command: 'pf build .' } };
  assert.match(evaluate(input, {}).context, /package-lock\.json/);
  assert.match(evaluate(input, { PF_GUARD_STRICT: '1' }).deny, /pf project freeze/);
});

test('pf build with a fresh or missing lock is silent', () => {
  const fresh = project();
  assert.equal(evaluate({ cwd: fresh.root, tool_name: 'Bash', tool_input: { command: 'pf build .' } }), null);
  const never = project({ frozen: false });
  assert.equal(evaluate({ cwd: never.root, tool_name: 'Bash', tool_input: { command: 'pf build .' } }), null);
});

test('PF_GUARD=off disables the hook', () => {
  const { root, manifest } = project();
  assert.equal(evaluate({ cwd: root, tool_name: 'Edit', tool_input: { file_path: manifest } }, { PF_GUARD: 'off' }), null);
});

test('render emits the Claude Code hook JSON shape', () => {
  const deny = JSON.parse(render({ event: 'PreToolUse', deny: 'x' }));
  assert.equal(deny.hookSpecificOutput.permissionDecision, 'deny');
  const ctx = JSON.parse(render({ event: 'PostToolUse', context: 'y' }));
  assert.equal(ctx.hookSpecificOutput.additionalContext, 'y');
  assert.equal(render(null), '');
});

test('CLI fails open on invalid stdin and exits 0', () => {
  const r = spawnSync(process.execPath, [SCRIPT], { input: 'not json', encoding: 'utf8' });
  assert.equal(r.status, 0);
  assert.equal(r.stdout, '');
});

test('CLI end-to-end prints JSON for a manifest edit', () => {
  const { root, manifest } = project();
  const input = JSON.stringify({ cwd: root, tool_name: 'Edit', tool_input: { file_path: manifest } });
  const r = spawnSync(process.execPath, [SCRIPT], { input, encoding: 'utf8' });
  assert.equal(r.status, 0);
  assert.match(JSON.parse(r.stdout).hookSpecificOutput.additionalContext, /pf project freeze/);
});

test('~/.platform-factory (pf user plugin dir) is not mistaken for a project', () => {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'pf-home-'));
  fs.mkdirSync(path.join(home, '.platform-factory'));
  fs.mkdirSync(path.join(home, 'work'));
  const saved = { HOME: process.env.HOME, USERPROFILE: process.env.USERPROFILE };
  process.env.HOME = home;
  process.env.USERPROFILE = home;
  try {
    // The fake home holds only pf's per-user dir: it must not be reported as a project root.
    const root = findProjectRoot(path.join(home, 'work'));
    assert.notEqual(root && fs.realpathSync.native(root), fs.realpathSync.native(home));
  } finally {
    for (const [k, v] of Object.entries(saved)) { if (v === undefined) delete process.env[k]; else process.env[k] = v; }
  }
});

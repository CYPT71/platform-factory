#!/usr/bin/env python3
"""Poll only failed default-branch CI; never execute PR/fork code or artifacts.
The trusted JSON configuration comes from an administrator's repository var.
"""
import hashlib
import json
import os
from pathlib import Path
import subprocess

root = Path(os.environ['GITHUB_WORKSPACE']).resolve()
repo = os.environ['GITHUB_REPOSITORY']
base = os.environ['PF_DEFAULT_BRANCH']
# Hosted runners are ephemeral: one scheduled attempt per checkout revision.
# Manual dispatch is an explicit retry, still subject to PR deduplication.
if os.environ.get('GITHUB_EVENT_NAME') == 'schedule':
    previous = json.loads(subprocess.check_output(['gh', 'run', 'list', '--repo', repo,
        '--workflow', 'ci-autofix.yml', '--branch', base, '--limit', '100',
        '--json', 'databaseId,headSha,status,conclusion']))
    current_head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root).decode().strip()
    if any(str(r['databaseId']) != os.environ['GITHUB_RUN_ID'] and r['headSha'] == current_head
           and r['status'] == 'completed' and r['conclusion'] in ('success', 'failure') for r in previous):
        raise SystemExit(0)
config = json.loads(os.environ['PF_AUTOFIX_CONFIG'])
# Repository and target are pinned by the workflow, not by a failure payload.
config['repository'] = str(root)
config['base'] = base
config['github_repository'] = repo
for key in ('fixer', 'reproduce'):
    config[key] = [arg.replace('${WORKSPACE}', str(root)) for arg in config[key]]
config['checks'] = [[arg.replace('${WORKSPACE}', str(root)) for arg in cmd] for cmd in config['checks']]
path = Path(os.environ['RUNNER_TEMP']) / 'pf-autofix.json'
path.write_text(json.dumps(config))
path.chmod(0o600)
runs = json.loads(subprocess.check_output(['gh', 'run', 'list', '--repo', repo, '--branch', base,
                                         '--status', 'failure', '--limit', '30',
                                         '--json', 'databaseId,headSha,name,event,headBranch']))
head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root).decode().strip()
allowed = {'ci-quality', 'security-analysis', 'kind-multinode-runtime', 'runtime-integration'}
for run in runs:
    if run['headSha'] != head or run['headBranch'] != base or run['event'] != 'push' or run['name'] not in allowed:
        continue
    fingerprint = hashlib.sha256(('ci:' + run['name'] + ':' + head).encode()).hexdigest()
    # State is ephemeral on hosted runners, so also deduplicate via open AND
    # closed PRs before every attempt. The concurrency group serializes polls.
    branch_id = hashlib.sha256((fingerprint + '\x00' + head).encode()).hexdigest()[:24]
    existing = json.loads(subprocess.check_output(['gh', 'pr', 'list', '--repo', repo, '--state', 'all',
                                                   '--head', 'autofix/' + branch_id, '--json', 'number']))
    if existing:
        continue
    event = dict(api_version='platform-factory.dev/hook-event/v1', id=fingerprint,
                 kind='command.failed', command='ci', exit_code=1, repository=str(root), duration_ms=0)
    # The runner independently reproduces the failure from this trusted checkout.
    result = subprocess.run([str(Path(os.environ['RUNNER_TEMP']) / 'pf'), 'hooks', 'autofix', '--config', str(path)],
                            input=json.dumps(event).encode(), cwd=root)
    if result.returncode:
        raise SystemExit(result.returncode)
    break

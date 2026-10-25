#!/usr/bin/env python3
"""Test-only runtime proxy: preserve evidence before conmon's OCI delete."""
import argparse
import json
import os
from pathlib import Path
import re
import shutil
import sys


def capture(arguments, evidence, environ):
    if 'delete' not in arguments:
        return
    identities = [arg for arg in arguments[arguments.index('delete') + 1:]
                  if re.fullmatch(r'[0-9a-f]{64}', arg)]
    if len(identities) != 1:
        return
    root = Path(environ.get('XDG_RUNTIME_DIR', '/run/user/' + str(os.geteuid()))) / 'platform-factory-runtime'
    for index, arg in enumerate(arguments):
        if arg == '--root' and index + 1 < len(arguments):
            root = Path(arguments[index + 1])
        elif arg.startswith('--root='):
            root = Path(arg.split('=', 1)[1])
    identity = identities[0]
    state = root / (identity + '.json')
    log = root / (identity + '.supervisor.log')
    evidence = Path(evidence)
    if state.is_file() and state.stat().st_size <= 1024 * 1024:
        # Decode before recording; the assertion on terminal metrics remains
        # in the lifecycle test, rather than silently accepting partial data.
        data = json.loads(state.read_text())
        if data.get('status') == 'stopped':
            (evidence / 'podman-microvm-terminal-state.json').write_text(json.dumps(data))
    if log.is_file() and log.stat().st_size <= 16 * 1024 * 1024:
        shutil.copyfile(log, evidence / 'podman-microvm-supervisor-final.log')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--runtime', required=True)
    parser.add_argument('--evidence', required=True)
    parser.add_argument('arguments', nargs=argparse.REMAINDER)
    options = parser.parse_args()
    args = options.arguments
    if args[:1] == ['--']:
        args = args[1:]
    try:
        capture(args, options.evidence, os.environ)
    except (OSError, ValueError):
        print('runtime proof: final evidence unavailable before delete', file=sys.stderr)
    os.execv(options.runtime, [options.runtime, *args])


if __name__ == '__main__':
    main()

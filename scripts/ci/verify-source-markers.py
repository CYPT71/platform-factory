#!/usr/bin/env python3
"""Reject unfinished implementation comments in tracked production Go sources.
Documentation, tests and generated plugin templates may describe unfinished work.
"""
from pathlib import Path
import re
import subprocess
import sys

files = subprocess.check_output(['git', 'ls-files', '-z', 'cmd', 'internal']).decode().split('\0')
# This file emits starter source for new language plugins; its markers belong
# to the generated project, not to the implementation of this repository.
source_generators = {'internal/mcp/plugins/create.go'}
failures = []
for name in files:
    if not name.endswith('.go') or name.endswith('_test.go') or name in source_generators:
        continue
    for number, line in enumerate(Path(name).read_text().splitlines(), 1):
        if re.match(r'^\s*(?://|/\*|\*)\s*(?:TODO|FIXME)\b', line):
            failures.append(f'{name}:{number}: {line.strip()}')
if failures:
    print('\n'.join(failures), file=sys.stderr)
    sys.exit(1)
print('SOURCE_MARKERS_OK')

#!/usr/bin/env python3
"""Write a non-secret, checksummed CI trace manifest for uploaded evidence."""
import hashlib
import json
import os
import pathlib
import sys
from datetime import datetime, timezone

if len(sys.argv) < 2:
    raise SystemExit("usage: write-traceability.py OUTPUT [EVIDENCE_FILE ...]")

output = pathlib.Path(sys.argv[1])
evidence = []
for name in sys.argv[2:]:
    path = pathlib.Path(name)
    if not path.is_file():
        continue
    data = path.read_bytes()
    evidence.append({
        "path": name,
        "bytes": len(data),
        "sha256": hashlib.sha256(data).hexdigest(),
    })

manifest = {
    "schema_version": 1,
    "time": datetime.now(timezone.utc).isoformat(),
    "level": "INFO",
    "component": "ci",
    "operation": "evidence-publication",
    "trace_id": os.getenv("GITHUB_RUN_ID", "local"),
    "source": {
        "repository": os.getenv("GITHUB_REPOSITORY", "local"),
        "commit": os.getenv("GITHUB_SHA", "local"),
        "ref": os.getenv("GITHUB_REF", "local"),
        "workflow": os.getenv("GITHUB_WORKFLOW", "local"),
        "run_id": os.getenv("GITHUB_RUN_ID", "local"),
        "run_attempt": os.getenv("GITHUB_RUN_ATTEMPT", "local"),
        "actor": os.getenv("GITHUB_ACTOR", "local"),
        "runner_os": os.getenv("RUNNER_OS", sys.platform),
        "runner_arch": os.getenv("RUNNER_ARCH", "local"),
    },
    "evidence": evidence,
}
output.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
print(
    f"level=INFO component=ci operation=evidence-publication "
    f"trace_id={manifest['trace_id']} files={len(evidence)} output={output}"
)

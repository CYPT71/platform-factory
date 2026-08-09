#!/usr/bin/env python3
"""Write one flattened record per family/parameter from a benchmark.json
report to an append-compatible JSONL artifact. The workflow deliberately
does not push generated history directly to a protected branch."""
import json
import pathlib
import sys

if len(sys.argv) != 3:
    raise SystemExit("usage: append-benchmark-history.py benchmark.json benchmark-history.jsonl")

report = json.loads(pathlib.Path(sys.argv[1]).read_text())
history_path = pathlib.Path(sys.argv[2])
env = report["environment"]

lines = []
for family, entries in report["families"].items():
    for entry in entries:
        record = {
            "schema_version": 1,
            "git_commit": env["git_commit"],
            "workflow_run_id": env["run_id"],
            "family": family,
            "parameter": entry["parameter"],
            "runs": entry["ns_per_op"]["runs"],
            "median_ns_per_op": entry["ns_per_op"]["median"],
            "median_bytes_per_op": entry["bytes_per_op"]["median"],
            "median_allocs_per_op": entry["allocs_per_op"]["median"],
        }
        if "mb_per_second" in entry:
            record["median_mb_per_second"] = entry["mb_per_second"]["median"]
        lines.append(json.dumps(record, sort_keys=True))

with history_path.open("a") as f:
    for line in lines:
        f.write(line + "\n")
print(f"BENCHMARK_HISTORY_APPENDED records={len(lines)} path={history_path}")

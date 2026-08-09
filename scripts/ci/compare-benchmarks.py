#!/usr/bin/env python3
"""Compare two benchmark.json reports (current branch vs. main) and flag
percentage regressions against fixed thresholds. Always exits 0: this is
informational evidence for a non-blocking CI job, not a merge gate - see
the wiki's Benchmarks page for why a single GitHub-hosted run isn't treated
as a hard pass/fail signal yet.
"""
import json
import pathlib
import sys

THROUGHPUT_DROP_THRESHOLD_PCT = 5.0   # median MB/s: flag if it drops more than this.
MEMORY_INCREASE_THRESHOLD_PCT = 5.0   # median B/op: flag if it rises more than this.
# Allocation counts are otherwise deterministic per code path (see
# BenchmarkBuild's stddev=0 allocs/op in practice); any change at all is
# worth a human look, not just changes past some percentage.

if len(sys.argv) != 4:
    raise SystemExit("usage: compare-benchmarks.py CURRENT.json BASELINE.json OUTPUT.md")

current = json.loads(pathlib.Path(sys.argv[1]).read_text())
baseline = json.loads(pathlib.Path(sys.argv[2]).read_text())


def pct_change(new, old):
    if old == 0:
        return None
    return (new - old) / old * 100


def index_by_parameter(report):
    out = {}
    for family, entries in report.get("families", {}).items():
        for entry in entries:
            out[(family, entry["parameter"])] = entry
    return out


current_by_key = index_by_parameter(current)
baseline_by_key = index_by_parameter(baseline)

rows = []
flags = []
for key in sorted(set(current_by_key) & set(baseline_by_key)):
    family, parameter = key
    cur, base = current_by_key[key], baseline_by_key[key]
    throughput_delta = None
    if "mb_per_second" in cur and "mb_per_second" in base:
        throughput_delta = pct_change(cur["mb_per_second"]["median"], base["mb_per_second"]["median"])
    memory_delta = pct_change(cur["bytes_per_op"]["median"], base["bytes_per_op"]["median"])
    allocs_delta = cur["allocs_per_op"]["median"] - base["allocs_per_op"]["median"]

    row_flags = []
    if throughput_delta is not None and throughput_delta < -THROUGHPUT_DROP_THRESHOLD_PCT:
        row_flags.append(f"throughput down {throughput_delta:.1f}%")
    if memory_delta is not None and memory_delta > MEMORY_INCREASE_THRESHOLD_PCT:
        row_flags.append(f"memory up {memory_delta:.1f}%")
    if allocs_delta != 0:
        row_flags.append(f"allocs/op {'+' if allocs_delta > 0 else ''}{allocs_delta:.0f}")
    if row_flags:
        flags.append(f"{family} {parameter}: " + ", ".join(row_flags))

    rows.append({
        "family": family,
        "parameter": parameter,
        "throughput_pct_change": throughput_delta,
        "memory_pct_change": memory_delta,
        "allocs_delta": allocs_delta,
        "flagged": bool(row_flags),
    })

lines = [
    "## Benchmark comparison: this run vs. baseline",
    "",
    f"Thresholds: throughput drop > {THROUGHPUT_DROP_THRESHOLD_PCT:.0f}%, "
    f"memory increase > {MEMORY_INCREASE_THRESHOLD_PCT:.0f}%, any allocs/op change. "
    "Informational only - see the wiki's Benchmarks page for why this doesn't gate merges yet.",
    "",
]
if flags:
    lines.append(f"**{len(flags)} entr{'y' if len(flags) == 1 else 'ies'} past threshold:**")
    lines += [f"- {f}" for f in flags]
else:
    lines.append("No entry crossed the regression thresholds.")
lines += [
    "",
    "| Family | Parameter | Throughput Δ | Memory Δ | Allocs/op Δ |",
    "| --- | --- | ---: | ---: | ---: |",
]
for r in rows:
    throughput_str = f"{r['throughput_pct_change']:+.1f}%" if r["throughput_pct_change"] is not None else "n/a"
    memory_str = f"{r['memory_pct_change']:+.1f}%" if r["memory_pct_change"] is not None else "n/a"
    marker = " ⚠️" if r["flagged"] else ""
    lines.append(f"| {r['family']} | {r['parameter']} | {throughput_str} | {memory_str} | {r['allocs_delta']:+.0f}{marker} |")
lines.append("")

pathlib.Path(sys.argv[3]).write_text("\n".join(lines) + "\n")
print(f"BENCHMARK_COMPARISON_OK compared={len(rows)} flagged={len(flags)}")

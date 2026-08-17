#!/usr/bin/env python3
"""Turn `go test -bench -benchmem -count=N` output into stable JSON and
Markdown CI evidence, with full statistics (not just the median) and
reproducibility metadata for every benchmark family this project runs:
BenchmarkBuild (end-to-end, disk included), BenchmarkMakeLayer (in-memory
only), BenchmarkNaiveTarGzip (bare tar+gzip reference), and
BenchmarkBuildParallel (fixed concurrency levels).
"""
import json
import math
import os
import pathlib
import platform
import re
import statistics
import sys


# The trailing -N is Go's GOMAXPROCS suffix; it's *omitted* when -cpu names
# exactly one value (e.g. our own -cpu=1 for the sequential families), so
# it must be optional here, not assumed present.
LINE = re.compile(
    r"^(Benchmark\S+?)(?:-\d+)?\s+\d+\s+"
    r"([\d.]+)\s+ns/op"
    r"(?:\s+([\d.]+)\s+MB/s)?"
    r"\s+(\d+)\s+B/op\s+(\d+)\s+allocs/op\s*$"
)
# BenchmarkBuild/payload_1024KiB -> ("BenchmarkBuild", "payload", "1024KiB")
# BenchmarkBuildParallel/concurrency_8 -> ("BenchmarkBuildParallel", "concurrency", "8")
SUBTEST = re.compile(r"^(Benchmark\w+)/(payload|concurrency)_(\S+)$")

if len(sys.argv) != 5:
    raise SystemExit("usage: benchmark-report.py INPUT ENVIRONMENT.txt OUTPUT.json OUTPUT.md")

input_path, environment_path, json_path, md_path = sys.argv[1:]


def percentile(sorted_values, pct):
    """Nearest-rank percentile; exact for n=1, stable and dependency-free."""
    if len(sorted_values) == 1:
        return sorted_values[0]
    rank = pct / 100 * (len(sorted_values) - 1)
    lower = math.floor(rank)
    upper = math.ceil(rank)
    if lower == upper:
        return sorted_values[lower]
    fraction = rank - lower
    return sorted_values[lower] + (sorted_values[upper] - sorted_values[lower]) * fraction


def stats(values):
    ordered = sorted(values)
    return {
        "runs": len(values),
        "mean": statistics.mean(values),
        "median": statistics.median(values),
        "min": ordered[0],
        "max": ordered[-1],
        "stddev": statistics.stdev(values) if len(values) >= 2 else 0.0,
        "p95": percentile(ordered, 95),
    }


samples = {}  # (family, param_kind, param_value) -> {"ns_per_op": [...], ...}
for line in pathlib.Path(input_path).read_text().splitlines():
    match = LINE.match(line.strip())
    if not match:
        continue
    full_name, ns, mbps, bytes_per_op, allocs = match.groups()
    sub = SUBTEST.match(full_name)
    if not sub:
        continue
    family, param_kind, param_value = sub.groups()
    key = (family, param_kind, param_value)
    bucket = samples.setdefault(key, {"ns_per_op": [], "mb_per_second": [], "bytes_per_op": [], "allocs_per_op": []})
    bucket["ns_per_op"].append(float(ns))
    if mbps is not None:
        bucket["mb_per_second"].append(float(mbps))
    bucket["bytes_per_op"].append(int(bytes_per_op))
    bucket["allocs_per_op"].append(int(allocs))

if not samples:
    raise SystemExit("no recognized Benchmark* samples found in " + input_path)


def sort_key(item):
    (family, param_kind, param_value), _ = item
    numeric = "".join(ch for ch in param_value if ch.isdigit())
    return (family, param_kind, int(numeric) if numeric else 0)


families = {}
for (family, param_kind, param_value), values in sorted(samples.items(), key=sort_key):
    entry = {
        "parameter": f"{param_kind}_{param_value}",
        "ns_per_op": stats(values["ns_per_op"]),
        "bytes_per_op": stats(values["bytes_per_op"]),
        "allocs_per_op": stats(values["allocs_per_op"]),
    }
    if values["mb_per_second"]:
        entry["mb_per_second"] = stats(values["mb_per_second"])
    if param_kind == "payload":
        digits = "".join(ch for ch in param_value if ch.isdigit())
        entry["payload_kib"] = int(digits) if digits else None
    else:
        entry["concurrency"] = int("".join(ch for ch in param_value if ch.isdigit()) or 0)
    families.setdefault(family, []).append(entry)

environment_text = pathlib.Path(environment_path).read_text() if pathlib.Path(environment_path).exists() else ""
environment = {
    "raw": environment_text.strip(),
    "python_reporting_host": {"system": platform.system(), "machine": platform.machine()},
    "go_num_cpu": os.environ.get("BENCHMARK_NUM_CPU", ""),
    "go_maxprocs": os.environ.get("BENCHMARK_GOMAXPROCS", ""),
    "cpu_fixed_at": os.environ.get("BENCHMARK_CPU_FIXED", ""),
    "benchmark_command": os.environ.get("BENCHMARK_COMMAND", ""),
    "git_commit": os.environ.get("GITHUB_SHA", "local"),
    "git_ref": os.environ.get("GITHUB_REF", "local"),
    "workflow": os.environ.get("GITHUB_WORKFLOW", "local"),
    "run_id": os.environ.get("GITHUB_RUN_ID", "local"),
    "run_attempt": os.environ.get("GITHUB_RUN_ATTEMPT", "local"),
    "runner_os": os.environ.get("RUNNER_OS", platform.system()),
    "runner_arch": os.environ.get("RUNNER_ARCH", platform.machine()),
}

report = {
    "schema_version": 2,
    "environment": environment,
    "families": families,
    "interpretation": (
        "Each entry summarizes N independent `go test -count` repetitions "
        "on the same runner image and Go toolchain within a single CI run "
        "(not independent hardware/campaigns). Compare only reports from "
        "the same runner image and Go version. GitHub-hosted runner "
        "variance makes this evidence observational, not a hard SLA - see "
        "the wiki's Benchmarks page for the full reproducibility and "
        "comparison-scope notes."
    ),
}
pathlib.Path(json_path).write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")

FAMILY_TITLES = {
    "BenchmarkBuild": "BenchmarkBuild (end-to-end: validation, tar, gzip, hash, disk write, atomic rename)",
    "BenchmarkMakeLayer": "BenchmarkMakeLayer (in-memory only: tar, gzip, hash - no disk I/O)",
    "BenchmarkNaiveTarGzip": "BenchmarkNaiveTarGzip (reference: bare archive/tar + compress/gzip, no OCI layout)",
    "BenchmarkBuildParallel": "BenchmarkBuildParallel (BenchmarkBuild at fixed concurrency levels)",
}

rows = [
    "# platform-factory-base benchmark",
    "",
    "Full statistics per benchmark, not just the median. Every family ran "
    "the same number of independent `-count` repetitions on this single "
    "CI run (see `environment` in benchmark.json for the exact command, "
    "Go version, CPU/GOMAXPROCS, commit, and workflow run).",
    "",
]
for family, entries in families.items():
    rows.append(f"## {FAMILY_TITLES.get(family, family)}")
    rows.append("")
    is_payload = "payload_kib" in entries[0]
    header_label = "Payload" if is_payload else "Concurrency"
    rows.append(f"| {header_label} | Runs | Time/op (median, p95) | Throughput (median) | Heap/op (median) | Allocs/op (median) |")
    rows.append("| ---: | ---: | ---: | ---: | ---: | ---: |")
    for e in entries:
        label = f"{e['payload_kib']} KiB" if is_payload else f"{e['concurrency']}x"
        ns = e["ns_per_op"]
        throughput = f"{e['mb_per_second']['median']:.2f} MB/s" if "mb_per_second" in e else "n/a"
        rows.append(
            f"| {label} | {ns['runs']} | {ns['median']/1_000_000:.3f} ms, p95 {ns['p95']/1_000_000:.3f} ms | "
            f"{throughput} | {e['bytes_per_op']['median']/1024:.1f} KiB | {e['allocs_per_op']['median']:.0f} |"
        )
    rows.append("")

rows += [
    "## Environment",
    "",
    "```",
    environment_text.strip() or "(not recorded)",
    "```",
    "",
    f"- Command: `{environment['benchmark_command'] or '(not recorded)'}`",
    f"- Fixed GOMAXPROCS for sequential families: `{environment['cpu_fixed_at'] or '(not recorded)'}`",
    f"- Git commit: `{environment['git_commit']}` ref `{environment['git_ref']}`",
    f"- Workflow: `{environment['workflow']}` run `{environment['run_id']}` attempt `{environment['run_attempt']}`",
    f"- Runner: `{environment['runner_os']}/{environment['runner_arch']}`",
    "",
    "> These are single-CI-run measurements on a shared GitHub-hosted "
    "runner: informative for spotting large regressions and understanding "
    "scaling behavior, but not a production latency SLA, and not a "
    "substitute for multiple independent campaigns on dedicated hardware. "
    "See the wiki's Benchmarks page.",
    "",
]
pathlib.Path(md_path).write_text("\n".join(rows) + "\n")

# Consistency check: everything in the Markdown and JSON was derived from
# the same `families`/`environment` dicts in this one process, but re-parse
# both outputs and cross-check the run counts and family/parameter set
# actually match what's in benchmark.json - guards against the two writers
# above silently drifting apart in a future change, not just today's output.
written_json = json.loads(pathlib.Path(json_path).read_text())
written_md = pathlib.Path(md_path).read_text()
for family, entries in written_json["families"].items():
    if FAMILY_TITLES.get(family, family) not in written_md:
        raise SystemExit(f"consistency check failed: {family} section missing from {md_path}")
    for e in entries:
        runs_str = str(e["ns_per_op"]["runs"])
        if f"| {runs_str} |" not in written_md:
            raise SystemExit(
                f"consistency check failed: {family} {e['parameter']} runs={runs_str} not found in {md_path}"
            )
print(f"BENCHMARK_REPORT_OK families={len(written_json['families'])}")

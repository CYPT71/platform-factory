#!/usr/bin/env python3
"""Repeat offline OCI packaging and tamper checks for a prebuilt Linux/amd64 binary."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import tempfile
import time


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ["pf", "builder", "binary", "results"]:
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--runs", type=int, default=10)
    args = parser.parse_args()
    if not __debug__ or args.runs < 1:
        parser.error("positive run count required; do not use Python -O")
    for name in ["pf", "builder", "binary"]:
        setattr(args, name, getattr(args, name).resolve(strict=True))
    out = args.results.resolve()
    out.mkdir(parents=True, exist_ok=False)
    env = {**os.environ, "GOPROXY": "off", "GOSUMDB": "off"}
    report = {
        "pf_sha256": digest(args.pf), "builder_sha256": digest(args.builder),
        "binary_sha256": digest(args.binary), "host": platform.platform(),
        "target": "linux/amd64",
        "network": "No registry, runtime, dependency resolver or source compilation invoked.",
        "scope": "Packaging one fixed executable; not source reproducibility, onboarding or adoption.",
        "trials": [],
    }

    def save():
        (out / "report.json").write_text(json.dumps(report, indent=2))

    for number in range(1, args.runs + 1):
        work = Path(tempfile.mkdtemp(prefix=f"case-{number:02}-", dir=out))
        logs, trial = [], {"number": number}
        started = time.monotonic()

        def run(label, argv, success=True):
            argv = [str(value) for value in argv]
            result = subprocess.run(argv, env=env, capture_output=True, text=True, timeout=120)
            logs.append({"label": label, "argv": argv, "exit_code": result.returncode,
                         "stdout": result.stdout, "stderr": result.stderr})
            if success and result.returncode:
                raise RuntimeError(f"{label}: {result.stderr}")
            return result

        try:
            for name in ["image-a", "image-b"]:
                run("package-" + name, [args.builder, "-binary", args.binary,
                                       "-output", work / name, "-arch", "amd64"])
                run("verify-" + name, [args.pf, "verify", work / name])
            manifests = [json.loads((work / name / "index.json").read_text())["manifests"][0]["digest"]
                         for name in ["image-a", "image-b"]]
            assert manifests[0] == manifests[1], manifests
            damaged = work / "image-tampered"
            shutil.copytree(work / "image-a", damaged)
            run("verify-before-mutation", [args.pf, "verify", damaged])
            blob = damaged / "blobs/sha256" / manifests[0].split(":")[1]
            blob.write_bytes(blob.read_bytes() + b"\n")
            rejection = run("verify-after-mutation", [args.pf, "verify", damaged], success=False)
            assert rejection.returncode != 0 and re.search("size|digest|blob", rejection.stderr, re.I), rejection.stderr
            trial.update(success=True, manifest_digest=manifests[0], tamper_rejected=True,
                         rejection=rejection.stderr.strip())
        except Exception as error:
            trial.update(success=False, error=str(error))
        finally:
            trial["seconds"] = round(time.monotonic() - started, 3)
            (out / f"trial-{number:02}.json").write_text(json.dumps(logs, indent=2))
            shutil.rmtree(work)
            trial["owned_temporary_directory_removed"] = not work.exists()
            report["trials"].append(trial)
            save()
        print(f"Trial {number}/{args.runs}: {'PASS' if trial['success'] else 'FAIL'}", flush=True)
    report["summary"] = {
        "passed": sum(t["success"] for t in report["trials"]),
        "attempted": len(report["trials"]),
        "identical_manifest_across_trials": len({t.get("manifest_digest") for t in report["trials"]}) == 1,
        "temporary_directories_removed": all(t["owned_temporary_directory_removed"] for t in report["trials"]),
    }
    save()
    print(json.dumps(report["summary"]))
    return 0 if (report["summary"]["passed"] == args.runs
                 and report["summary"]["identical_manifest_across_trials"]
                 and report["summary"]["temporary_directories_removed"]) else 1


if __name__ == "__main__":
    raise SystemExit(main())

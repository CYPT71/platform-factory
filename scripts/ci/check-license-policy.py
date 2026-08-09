#!/usr/bin/env python3
"""Create a deterministic Go module/license inventory and enforce its policy."""
import argparse
import json
import pathlib
import sys


def documents(stream):
    decoder = json.JSONDecoder()
    content = stream.read()
    offset = 0
    while offset < len(content):
        while offset < len(content) and content[offset].isspace():
            offset += 1
        if offset == len(content):
            return
        value, offset = decoder.raw_decode(content, offset)
        yield value


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--policy", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument(
        "--packages",
        action="store_true",
        help="read go list -deps -test -json package documents and inventory their modules",
    )
    arguments = parser.parse_args()
    policy = json.loads(pathlib.Path(arguments.policy).read_text(encoding="utf-8"))
    allowed = set(policy["allowed_licenses"])
    assignments = policy["modules"]
    inventory = []
    failures = []
    modules = []
    seen = set()
    for document in documents(sys.stdin):
        module = document.get("Module") if arguments.packages else document
        if not module:
            continue
        path = module.get("Path")
        if not path or path in seen:
            continue
        seen.add(path)
        modules.append(module)
    for module in modules:
        path = module["Path"]
        version = module.get("Version") or "workspace"
        license_name = assignments.get(path)
        if license_name is None:
            failures.append(f"module {path}@{version} has no reviewed license assignment")
            license_name = "UNKNOWN"
        elif license_name not in allowed:
            failures.append(f"module {path}@{version} uses forbidden license {license_name}")
        inventory.append({"license": license_name, "module": path, "version": version})
    inventory.sort(key=lambda item: (item["module"], item["version"]))
    report = {
        "allowed_licenses": sorted(allowed),
        "modules": inventory,
        "status": "pass" if not failures else "fail",
        "violations": failures,
    }
    pathlib.Path(arguments.output).write_text(
        json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    if failures:
        for failure in failures:
            print(f"LICENSE_POLICY_FAILURE: {failure}", file=sys.stderr)
        raise SystemExit(1)
    print(f"LICENSE_POLICY_OK modules={len(inventory)}")


if __name__ == "__main__":
    main()

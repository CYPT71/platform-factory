#!/usr/bin/env python3
"""Prove that the OCI verifier rejects representative hostile layouts."""
import json
import pathlib
import shutil
import subprocess
import sys
import tempfile


def reject(verifier, layout, case):
    result = subprocess.run([sys.executable, verifier, str(layout)], capture_output=True, text=True)
    if result.returncode == 0:
        raise SystemExit(f"false negative ({case}): verifier accepted invalid layout")
    print(f"OCI_NEGATIVE_CASE_REJECTED: {case}")


def mutate(root, case):
    blobs = sorted((root / "blobs" / "sha256").iterdir())
    if case == "missing-blob":
        blobs[0].unlink()
    elif case == "corrupted-blob":
        blobs[0].write_bytes(blobs[0].read_bytes() + b"x")
    elif case == "truncated-layer":
        blobs[-1].write_bytes(blobs[-1].read_bytes()[:8])
    elif case == "invalid-index":
        (root / "index.json").write_text('{"schemaVersion":2,"manifests":[]}', encoding="utf-8")
    elif case == "extra-blob":
        (root / "blobs" / "sha256" / ("0" * 64)).write_bytes(b"unexpected")
    elif case == "symlink-blob":
        blobs[0].unlink()
        (root / "blobs" / "sha256" / blobs[0].name).symlink_to("/etc/passwd")
    elif case == "invalid-media-type":
        document = json.loads((root / "index.json").read_text(encoding="utf-8"))
        document["manifests"][0]["mediaType"] = "application/octet-stream"
        (root / "index.json").write_text(json.dumps(document), encoding="utf-8")
    else:
        raise ValueError(case)


def main(verifier, source):
    cases = ("missing-blob", "corrupted-blob", "truncated-layer", "invalid-index", "extra-blob", "symlink-blob", "invalid-media-type")
    with tempfile.TemporaryDirectory(prefix="oci-negative-") as temporary:
        for case in cases:
            destination = pathlib.Path(temporary) / case
            shutil.copytree(source, destination, symlinks=True)
            mutate(destination, case)
            reject(verifier, destination, case)


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit("usage: negative-oci-layout.py VERIFY_SCRIPT LAYOUT")
    main(sys.argv[1], sys.argv[2])

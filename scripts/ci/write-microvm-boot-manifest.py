#!/usr/bin/env python3
"""Write a deterministic, safely encoded microVM boot-bundle manifest."""
import argparse
import hashlib
import json
import pathlib


def digest(path):
    return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--architecture", required=True)
    parser.add_argument("--layer-digest", required=True)
    parser.add_argument("--init", required=True)
    parser.add_argument("--kernel", required=True)
    parser.add_argument("--kernel-provenance")
    parser.add_argument("--output", required=True)
    parser.add_argument("entrypoint", nargs=argparse.REMAINDER)
    arguments = parser.parse_args()
    entrypoint = arguments.entrypoint
    if entrypoint[:1] == ["--"]:
        entrypoint = entrypoint[1:]
    if not entrypoint:
        raise SystemExit("boot manifest requires a non-empty entrypoint")
    provenance = None
    if arguments.kernel_provenance:
        provenance_path = pathlib.Path(arguments.kernel_provenance)
        if provenance_path.is_file():
            provenance = json.loads(provenance_path.read_text(encoding="utf-8"))
    init_digest = digest(arguments.init)
    kernel_digest = digest(arguments.kernel)
    identity = {
        "entrypoint": entrypoint,
        "kernel_image_sha256": kernel_digest,
        "microvm_init_sha256": init_digest,
        "oci_layer_digest": arguments.layer_digest,
    }
    combined = hashlib.sha256(
        json.dumps(identity, separators=(",", ":"), sort_keys=True).encode()
    ).hexdigest()
    document = {
        "schema_version": 1,
        "component": "microvm-boot-bundle",
        "image_architecture": arguments.architecture,
        **identity,
        "kernel_provenance": provenance,
        "combined_digest": f"sha256:{combined}",
    }
    pathlib.Path(arguments.output).write_text(
        json.dumps(document, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    print(f"MICROVM_BOOT_MANIFEST_OK combined_digest=sha256:{combined}")


if __name__ == "__main__":
    main()

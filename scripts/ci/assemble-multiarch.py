#!/usr/bin/env python3
"""Assemble validated single-platform layouts into one deterministic OCI index."""
import hashlib
import json
import pathlib
import sys


def fail(message):
    raise SystemExit(f"MULTIARCH_ASSEMBLY_FAILURE: {message}")


def load(path):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        fail(f"cannot read {path}: {error}")


def blob(root, descriptor):
    digest = descriptor.get("digest", "")
    size = descriptor.get("size")
    if not digest.startswith("sha256:") or len(digest) != 71:
        fail(f"invalid digest {digest!r}")
    path = root / "blobs" / "sha256" / digest[7:]
    try:
        data = path.read_bytes()
    except OSError as error:
        fail(f"missing blob {digest}: {error}")
    if hashlib.sha256(data).hexdigest() != digest[7:] or len(data) != size:
        fail(f"digest or size mismatch for {digest}")
    return data


def main(output_name, input_names):
    output = pathlib.Path(output_name)
    if output.exists() or len(input_names) < 2:
        fail("output must not exist and at least two input layouts are required")
    descriptors = []
    platforms = set()
    all_blobs = {}
    for input_name in input_names:
        root = pathlib.Path(input_name)
        index = load(root / "index.json")
        manifests = index.get("manifests")
        if index.get("schemaVersion") != 2 or not isinstance(manifests, list) or len(manifests) != 1:
            fail(f"{root} is not a single-platform OCI layout")
        descriptor = manifests[0]
        platform = descriptor.get("platform", {})
        key = (platform.get("os"), platform.get("architecture"))
        if key[0] != "linux" or key[1] not in ("amd64", "arm64") or key in platforms:
            fail(f"unsupported or duplicate platform {key}")
        platforms.add(key)
        manifest_data = blob(root, descriptor)
        manifest = json.loads(manifest_data)
        referenced = [descriptor, manifest.get("config", {})] + manifest.get("layers", [])
        for item in referenced:
            data = blob(root, item)
            digest = item["digest"][7:]
            if digest in all_blobs and all_blobs[digest] != data:
                fail(f"content collision for sha256:{digest}")
            all_blobs[digest] = data
        descriptors.append(descriptor)
    descriptors.sort(key=lambda item: (item["platform"]["os"], item["platform"]["architecture"]))
    (output / "blobs" / "sha256").mkdir(parents=True)
    (output / "oci-layout").write_text('{"imageLayoutVersion":"1.0.0"}\n', encoding="utf-8")
    index_data = json.dumps(
        {"schemaVersion": 2, "manifests": descriptors},
        separators=(",", ":"),
        sort_keys=True,
    ) + "\n"
    (output / "index.json").write_text(index_data, encoding="utf-8")
    for digest, data in sorted(all_blobs.items()):
        (output / "blobs" / "sha256" / digest).write_bytes(data)
    print(f"MULTIARCH_ASSEMBLY_OK platforms={len(platforms)} blobs={len(all_blobs)}")


if __name__ == "__main__":
    if len(sys.argv) < 4:
        fail("usage: assemble-multiarch.py OUTPUT LAYOUT...")
    main(sys.argv[1], sys.argv[2:])

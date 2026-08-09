#!/usr/bin/env python3
"""Require a published OCI index to carry BuildKit attestation manifests."""
import json
import pathlib
import re
import sys

if len(sys.argv) != 2:
    raise SystemExit(f"usage: {sys.argv[0]} PUBLISHED_INDEX_JSON")
try:
    document = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
    manifests = document["manifests"]
except (OSError, json.JSONDecodeError, KeyError, TypeError) as exc:
    raise SystemExit(f"ATTESTATION_VALIDATION_FAILURE: invalid OCI index: {exc}")
if not isinstance(manifests, list):
    raise SystemExit("ATTESTATION_VALIDATION_FAILURE: manifests is not an array")
attestations = [
    descriptor
    for descriptor in manifests
    if isinstance(descriptor, dict)
    and descriptor.get("annotations", {}).get("vnd.docker.reference.type") == "attestation-manifest"
    and re.fullmatch(r"sha256:[0-9a-f]{64}", descriptor.get("digest", ""))
]
if not attestations:
    raise SystemExit("ATTESTATION_VALIDATION_FAILURE: no BuildKit attestation manifest")
print(f"ATTESTATION_VALIDATION_OK manifests={len(attestations)}")

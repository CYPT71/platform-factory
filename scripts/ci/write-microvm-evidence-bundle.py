#!/usr/bin/env python3
"""Merge microVM kernel provenance, a CycloneDX SBOM, a hardening report, and
a boot-bundle manifest into one file so Cosign can sign it as a single blob."""
import json
import sys

if len(sys.argv) != 6:
    raise SystemExit(
        "usage: write-microvm-evidence-bundle.py "
        "KERNEL_PROVENANCE KERNEL_SBOM HARDENING_REPORT BOOT_MANIFEST OUTPUT"
    )

provenance_path, sbom_path, hardening_report_path, boot_manifest_path, output_path = sys.argv[1:]
bundle = {
    "schema_version": 1,
    "kernel_provenance": json.load(open(provenance_path)),
    "kernel_sbom": json.load(open(sbom_path)),
    "kernel_hardening_report": json.load(open(hardening_report_path)),
    "boot_bundle": json.load(open(boot_manifest_path)),
}
with open(output_path, "w") as f:
    json.dump(bundle, f, indent=2, sort_keys=True)
    f.write("\n")

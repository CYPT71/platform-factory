#!/usr/bin/env python3
"""Strict, dependency-free verifier for an OCI image layout or image index."""
import gzip
import hashlib
import json
import pathlib
import tarfile
import sys

SHA256_DIGEST_LENGTH = 71
MANIFEST_MEDIA_TYPE = "application/vnd.oci.image.manifest.v1+json"
CONFIG_MEDIA_TYPE = "application/vnd.oci.image.config.v1+json"
LAYER_MEDIA_TYPE = "application/vnd.oci.image.layer.v1.tar+gzip"


def fail(message):
    print(f"OCI_VALIDATION_FAILURE: {message}", file=sys.stderr)
    raise SystemExit(1)


def read_regular(path, description):
    try:
        path.lstat()
    except OSError as error:
        fail(f"missing {description}: {error}")
    if not path.is_file() or path.is_symlink():
        fail(f"{description} is not a regular file")
    return path.read_bytes()


def parse_json(data, description):
    try:
        value = json.loads(data)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        fail(f"invalid {description}: {error}")
    if not isinstance(value, dict):
        fail(f"{description} must be a JSON object")
    return value


def descriptor(root, value, expected_media=None):
    if not isinstance(value, dict):
        fail("descriptor must be an object")
    digest = value.get("digest")
    size = value.get("size")
    if not isinstance(digest, str) or len(digest) != SHA256_DIGEST_LENGTH or not digest.startswith("sha256:"):
        fail(f"invalid descriptor digest: {digest!r}")
    if any(character not in "0123456789abcdef" for character in digest[7:]):
        fail(f"descriptor digest is not lowercase hexadecimal: {digest!r}")
    if not isinstance(size, int) or isinstance(size, bool) or size < 0:
        fail(f"invalid descriptor size: {size!r}")
    if expected_media and value.get("mediaType") != expected_media:
        fail(f"unexpected media type: {value.get('mediaType')!r}")
    data = read_regular(root / "blobs" / "sha256" / digest[7:], f"blob {digest}")
    if hashlib.sha256(data).hexdigest() != digest[7:]:
        fail(f"digest mismatch: {digest}")
    if size != len(data):
        fail(f"size mismatch: {digest}")
    return data, digest[7:]


def validate_layer(data, expected_diff_id, entrypoint):
    try:
        raw = gzip.decompress(data)
    except (OSError, EOFError) as error:
        fail(f"invalid gzip layer: {error}")
    if "sha256:" + hashlib.sha256(raw).hexdigest() != expected_diff_id:
        fail("layer diff_id does not match uncompressed layer")
    try:
        archive = tarfile.open(fileobj=__import__("io").BytesIO(raw), mode="r:")
    except tarfile.TarError as error:
        fail(f"invalid tar layer: {error}")
    names = set()
    for member in archive:
        name = member.name.rstrip("/")
        if not name or name in names or name.startswith("/") or ".." in pathlib.PurePosixPath(name).parts:
            fail(f"unsafe or duplicate tar path: {member.name!r}")
        names.add(name)
        if member.issym() or member.islnk() or member.isdev() or member.isfifo():
            fail(f"unsafe tar entry type: {member.name!r}")
    if entrypoint.lstrip("/") not in names:
        fail("entrypoint is absent from layer")


def main(root_name):
    root = pathlib.Path(root_name)
    if not root.is_dir() or root.is_symlink():
        fail("layout root is not a directory")
    marker = read_regular(root / "oci-layout", "oci-layout marker")
    if marker != b'{"imageLayoutVersion":"1.0.0"}\n':
        fail("invalid oci-layout marker")
    index = parse_json(read_regular(root / "index.json", "index"), "index")
    manifests = index.get("manifests")
    if index.get("schemaVersion") != 2 or not isinstance(manifests, list) or not manifests:
        fail("index must contain at least one schema-2 manifest")
    expected = set()
    seen_references = set()
    for manifest_descriptor in manifests:
        manifest_data, manifest_name = descriptor(root, manifest_descriptor, MANIFEST_MEDIA_TYPE)
        expected.add(manifest_name)
        platform = manifest_descriptor.get("platform")
        if not isinstance(platform, dict) or platform.get("os") != "linux" or platform.get("architecture") not in ("amd64", "arm64"):
            fail("index descriptor has an unsupported platform")
        annotations = manifest_descriptor.get("annotations", {})
        if not isinstance(annotations, dict):
            fail("index descriptor annotations must be an object")
        reference = annotations.get("org.opencontainers.image.ref.name", "")
        if not isinstance(reference, str):
            fail("image reference annotation must be a string")
        reference_platform = (reference, platform["os"], platform["architecture"])
        if reference_platform in seen_references:
            fail(f"duplicate image reference/platform: {reference_platform!r}")
        seen_references.add(reference_platform)

        manifest = parse_json(manifest_data, "manifest")
        layers = manifest.get("layers")
        if manifest.get("schemaVersion") != 2 or not isinstance(layers, list) or len(layers) != 1:
            fail("manifest must contain exactly one layer")
        config_data, config_name = descriptor(root, manifest.get("config"), CONFIG_MEDIA_TYPE)
        expected.add(config_name)
        layer_data, layer_name = descriptor(root, layers[0], LAYER_MEDIA_TYPE)
        expected.add(layer_name)
        config = parse_json(config_data, "config")
        if config.get("os") != platform["os"] or config.get("architecture") != platform["architecture"]:
            fail("config platform does not match index descriptor")
        rootfs = config.get("rootfs")
        runtime = config.get("config")
        if not isinstance(rootfs, dict) or rootfs.get("type") != "layers" or not isinstance(rootfs.get("diff_ids"), list) or len(rootfs["diff_ids"]) != 1:
            fail("config must contain exactly one rootfs diff_id")
        if not isinstance(runtime, dict) or not isinstance(runtime.get("Entrypoint"), list) or len(runtime["Entrypoint"]) != 1 or not isinstance(runtime["Entrypoint"][0], str):
            fail("config must contain exactly one entrypoint")
        validate_layer(layer_data, rootfs["diff_ids"][0], runtime["Entrypoint"][0])
    blob_dir = root / "blobs" / "sha256"
    actual = {item.name for item in blob_dir.iterdir() if item.is_file() and not item.is_symlink()}
    if actual != expected:
        fail("blob directory contains missing, extra, or non-regular blobs")
    print("OCI_VALIDATION_OK")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        fail("usage: verify-oci-layout.py LAYOUT")
    main(sys.argv[1])

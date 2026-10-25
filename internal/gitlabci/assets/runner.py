"""Fail-closed GitLab build/package/boot orchestration. No shell evaluation."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess


def read_json(path, limit=65536):
    with open(path, "rb") as stream:
        raw = stream.read(limit + 1)
    if len(raw) > limit:
        raise ValueError("JSON input exceeds byte budget")
    return json.loads(raw)


def digest(path):
    h = hashlib.sha256()
    with open(path, "rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(chunk)
    return "sha256:" + h.hexdigest()


def run(pf, config, phase):
    root = Path.cwd().resolve()
    def local(value):
        if not isinstance(value, str) or not value or "\\" in value:
            raise ValueError("expected a relative project path")
        p = Path(value)
        if p.is_absolute() or str(p) != value or ".." in p.parts or value == ".":
            raise ValueError("noncanonical project path")
        for parent in [p, *p.parents]:
            if (root / parent).is_symlink():
                raise ValueError("symlink project path")
        target = root / p
        if not target.resolve().is_relative_to(root):
            raise ValueError("path escapes checkout")
        return target
    cfg = read_json(local(config))
    required = {"version", "project", "inputs_lock", "layout", "output", "platform", "kernel", "kernel_digest", "init", "init_digest", "ready"}
    if set(cfg) != required or cfg["version"] != 1:
        raise ValueError("unsupported or incomplete CI config")
    if cfg["project"] != "pf.yaml":
        raise ValueError("component requires pf.yaml at checkout root")
    paths = {k: local(cfg[k]) for k in ("project", "inputs_lock", "layout", "output", "kernel", "init")}
    if cfg["platform"] not in ("linux/amd64", "linux/arm64"):
        raise ValueError("unsupported platform")
    for k in ("kernel_digest", "init_digest"):
        if not isinstance(cfg[k], str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", cfg[k]):
            raise ValueError("explicit SHA-256 pins required")
    if not isinstance(cfg["ready"], str) or not 1 <= len(cfg["ready"].encode()) <= 256:
        raise ValueError("literal readiness marker required")
    if cfg["layout"] != "dist/oci" or cfg["output"] != "dist/microvm":
        raise ValueError("component artifacts require dist/oci and dist/microvm")
    inputs = read_json(paths["inputs_lock"], 8 << 20)
    selected = [local(p) for p in inputs["paths"]]
    for output in (paths["layout"], paths["output"]):
        for source in selected:
            if output == source or output.is_relative_to(source) or source.is_relative_to(output):
                raise ValueError("outputs overlap locked inputs")
    required_inputs = [local(config), paths["project"], paths["project"].parent / "pf.lock", paths["project"].parent / ".platform-factory/freeze.lock.json", paths["kernel"], paths["init"]]
    for required_input in required_inputs:
        if not any(required_input == source or required_input.is_relative_to(source) for source in selected):
            raise ValueError("CI config, project lock, freeze inventory and boot inputs must be locked")
    lock_digest = digest(paths["inputs_lock"])
    def call(*args, capture=False):
        return subprocess.run([pf, *map(str, args)], check=True, timeout=900,
                              text=True, capture_output=capture)
    def verify():
        if digest(paths["inputs_lock"]) != lock_digest:
            raise ValueError("input lock changed during execution")
        call("ci", "verify", "--lock", paths["inputs_lock"])
        call("ci", "check", "--config", paths["project"], "--distroless")
    verify()
    # Packaging helpers and kernels are pinned independently of the OCI application.
    for name in ("kernel", "init"):
        if digest(paths[name]) != cfg[name + "_digest"]:
            raise ValueError(name + " digest mismatch")
    project = json.loads(call("project", "show", "--config", paths["project"], capture=True).stdout)["config"]
    if project.get("profile") != "static" or project.get("user") not in ("65532", "65532:65532") or project.get("include_project", False) or project.get("include") or project.get("shared_deps"):
        raise ValueError("CI distroless requires static profile, UID 65532 and no extra files")
    if local(project["output"]) != paths["layout"] or project.get("platform") != cfg["platform"]:
        raise ValueError("project output/platform differs from CI config")
    out = paths["output"]
    boot_evidence = None
    if phase == "build":
        if paths["layout"].exists() or out.exists():
            raise ValueError("fresh build outputs required")
        call("project", "build", "--config", paths["project"], "--yes")
    elif phase == "package":
        out.mkdir(exist_ok=False)
        result = call("ci", "package", "--layout", paths["layout"], "--platform", cfg["platform"], "--init", paths["init"], "--output", out / "initramfs.cpio.gz", capture=True)
        evidence = json.loads(result.stdout)
        evidence.update({"kernel_digest": cfg["kernel_digest"], "init_digest": cfg["init_digest"], "initramfs_digest": digest(out / "initramfs.cpio.gz"), "platform": cfg["platform"], "inputs_lock_digest": digest(paths["inputs_lock"])})
        (out / "package.json").write_text(json.dumps(evidence, sort_keys=True) + "\n")
    elif phase == "boot":
        if (out / "boot.json").exists():
            raise ValueError("fresh boot evidence output required")
        evidence = read_json(out / "package.json")
        if evidence["kernel_digest"] != cfg["kernel_digest"] or evidence["inputs_lock_digest"] != digest(paths["inputs_lock"]):
            raise ValueError("package evidence differs from locked inputs")
        call("ci", "boot", "--kernel", paths["kernel"], "--kernel-digest", cfg["kernel_digest"], "--initramfs", out / "initramfs.cpio.gz", "--initramfs-digest", evidence["initramfs_digest"], "--ready", cfg["ready"])
        boot_evidence = {"native_boot": True, "package_digest": digest(out / "package.json")}
    else:
        raise ValueError("unknown phase")
    verify()
    if boot_evidence is not None:
        with (out / "boot.json").open("x") as stream:
            stream.write(json.dumps(boot_evidence) + "\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--pf", required=True)
    parser.add_argument("--config", required=True)
    parser.add_argument("--phase", choices=("build", "package", "boot"), required=True)
    args = parser.parse_args()
    try:
        run(args.pf, args.config, args.phase)
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        parser.exit(1, "GitLab phase rejected: " + str(error) + "\n")

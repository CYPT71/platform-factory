#!/usr/bin/env python3
"""Semantically validate GitHub Actions workflow security invariants."""
import json
import pathlib
import re
import subprocess
import sys

RUBY_YAML_TO_JSON = r'''
require "yaml"
require "json"
path = ARGV.fetch(0)
puts JSON.generate(YAML.safe_load(File.read(path), aliases: false))
'''
def load_workflow(path: pathlib.Path):
    result = subprocess.run(
        ["ruby", "-ryaml", "-rjson", "-e", RUBY_YAML_TO_JSON, str(path)],
        check=False, capture_output=True, text=True,
    )
    if result.returncode:
        raise ValueError(result.stderr.strip() or "YAML parser failed")
    return json.loads(result.stdout)


def walk(value):
    if isinstance(value, dict):
        yield value
        for child in value.values():
            yield from walk(child)
    elif isinstance(value, list):
        for child in value:
            yield from walk(child)


# A step invokes the Go toolchain, and so can silently fetch a network
# toolchain unless GOTOOLCHAIN=local pins it to the version actions/setup-go
# installed, if "go" is the command actually being run: at the start of a
# line, after an env-var prefix (FOO=bar go build), after a shell separator
# (&&, ||, ;, then the same set of separators), or inside a $()/`` command
# substitution (which always executes, quoted or not).
_GO_VERBS = r"(build|test|vet|run|install|env)"
_GO_SUBSHELL_RE = re.compile(r"[`$]\(?\s*go\s+" + _GO_VERBS + r"\b")
_GO_BARE_RE = re.compile(r"go\s+" + _GO_VERBS + r"\b")
_GO_ENV_PREFIX_RE = re.compile(r"(?:\s*[A-Za-z_][A-Za-z0-9_]*=\S+\s+)*")


def _at_command_position(prefix: str) -> bool:
    stripped = prefix.rstrip()
    if not stripped or stripped[-1] in ";&|(":
        return True
    return bool(_GO_ENV_PREFIX_RE.fullmatch(prefix))


def invokes_go_toolchain(command: str) -> bool:
    for line in command.splitlines():
        if _GO_SUBSHELL_RE.search(line):
            return True
        for match in _GO_BARE_RE.finditer(line):
            prefix = line[: match.start()]
            if not _at_command_position(prefix):
                continue
            if prefix.count('"') % 2 == 1:
                continue  # inside a double-quoted string literal, not a real invocation
            return True
    return False


errors = []
allowed_runners = {"ubuntu-24.04", "macos-15", "windows-2025"}
for workflow in sorted(pathlib.Path(".github/workflows").glob("*.y*ml")):
    try:
        data = load_workflow(workflow)
    except (ValueError, json.JSONDecodeError) as exc:
        errors.append(f"{workflow}: invalid YAML: {exc}")
        continue
    if not isinstance(data, dict):
        errors.append(f"{workflow}: root must be a mapping")
        continue
    triggers = data.get("on", data.get(True))
    trigger_names = set(triggers) if isinstance(triggers, dict) else set(triggers or [])
    if {"pull_request_target", "workflow_run"} & trigger_names:
        errors.append(f"{workflow}: privileged untrusted trigger")
    jobs = data.get("jobs")
    if not isinstance(jobs, dict) or not jobs:
        errors.append(f"{workflow}: no jobs defined")
        continue
    for job_name, job in jobs.items():
        if not isinstance(job, dict):
            errors.append(f"{workflow}: job {job_name} must be a mapping")
            continue
        runner = job.get("runs-on")
        if runner == "${{ matrix.os }}":
            matrix = job.get("strategy", {}).get("matrix", {})
            matrix_runners = matrix.get("os") if isinstance(matrix, dict) else None
            if not isinstance(matrix_runners, list) or not matrix_runners or \
                    any(candidate not in allowed_runners for candidate in matrix_runners):
                errors.append(
                    f"{workflow}: job {job_name} runner matrix must contain only pinned supported images"
                )
        elif runner not in allowed_runners:
            errors.append(
                f"{workflow}: job {job_name} must use a pinned supported runner image"
            )
        if not isinstance(job.get("timeout-minutes"), int):
            errors.append(f"{workflow}: job {job_name} must declare integer timeout")
        job_env = job.get("env") if isinstance(job.get("env"), dict) else {}
        for step in job.get("steps", []):
            if not isinstance(step, dict):
                errors.append(f"{workflow}: job {job_name} contains invalid step")
                continue
            action = step.get("uses")
            if isinstance(action, str):
                if action.startswith(("./", "docker://")):
                    errors.append(f"{workflow}: disallowed action source {action}")
                elif not re.fullmatch(r"[^@\s]+@[0-9a-f]{40}", action):
                    errors.append(f"{workflow}: action is not SHA pinned: {action}")
            command = step.get("run")
            if isinstance(command, str) and "set -euo pipefail" not in command:
                errors.append(f"{workflow}: job {job_name} shell step lacks strict mode")
            if isinstance(command, str) and invokes_go_toolchain(command):
                step_env = step.get("env") if isinstance(step.get("env"), dict) else {}
                if {**job_env, **step_env}.get("GOTOOLCHAIN") != "local":
                    errors.append(
                        f"{workflow}: job {job_name} step {step.get('name')!r} invokes "
                        "the Go toolchain without pinning GOTOOLCHAIN=local"
                    )
        for node in walk(job):
            if isinstance(node, str) and re.search(r"\$\{\{\s*github\.event\.pull_request\.(title|body|head\.ref)", node):
                errors.append(f"{workflow}: untrusted PR value interpolated into shell")
if errors:
    print("WORKFLOW_VALIDATION_FAILURE", *errors, sep="\n", file=sys.stderr)
    sys.exit(1)
print("WORKFLOW_VALIDATION_OK")

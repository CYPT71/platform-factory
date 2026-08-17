#!/usr/bin/env python3
"""Docker/Podman monitoring plugin built with the official PF Python SDK."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
from typing import Any

# A published plugin installs `platform-factory-plugin` normally. This path
# shim makes the repository example runnable without pip or network access.
sys.path.insert(
    0,
    os.path.join(os.path.dirname(__file__), "..", "..", "..", "sdk", "plugin-python"),
)

from secure_oci_plugin import CAPABILITY, RPCError, RequestContext, Server  # noqa: E402


server = Server("lazy-docker-python", "1.0.0")


def select_engine(requested: str = "auto") -> str:
    if requested not in ("auto", "docker", "podman"):
        raise RPCError(400, "engine must be auto, docker, or podman")
    candidates = ("docker", "podman") if requested == "auto" else (requested,)
    for candidate in candidates:
        if shutil.which(candidate):
            return candidate
    raise RPCError(503, "Docker or Podman was not found on PATH")


def engine_json_lines(engine: str, arguments: list[str]) -> list[dict[str, Any]]:
    completed = subprocess.run(
        [engine, *arguments], check=False, capture_output=True, text=True, timeout=10
    )
    if completed.returncode != 0:
        detail = completed.stderr.strip() or completed.stdout.strip()
        raise RPCError(502, f"{engine} command failed: {detail}")
    try:
        return [json.loads(line) for line in completed.stdout.splitlines() if line.strip()]
    except json.JSONDecodeError as error:
        raise RPCError(502, "container engine returned malformed JSON") from error


def normalized_containers(engine: str) -> list[dict[str, str]]:
    native = engine_json_lines(engine, ["ps", "--all", "--format", "{{json .}}"])
    result = [
        {
            "id": str(row.get("ID") or row.get("Id") or ""),
            "name": str(row.get("Names") or row.get("Name") or ""),
            "image": str(row.get("Image") or ""),
            "state": str(row.get("State") or row.get("Status") or "unknown"),
            "ports": str(row.get("Ports") or ""),
        }
        for row in native
    ]
    return sorted(result, key=lambda item: (item["name"], item["id"]))


@server.handle("detect")
def detect(_params: dict[str, Any]) -> dict[str, Any]:
    """The example is a runtime plugin, not a language detector."""
    return {"kind": "unknown", "evidence": []}


@server.handle("freeze")
def freeze(_params: dict[str, Any]) -> dict[str, Any]:
    # Harmless baseline step required by the cross-language conformance
    # contract. Runtime monitoring itself never asks the host to execute it.
    return {"steps": [["python3", "--version"]], "profile": "runtime-monitor"}


@server.handle("plan")
def plan(_params: dict[str, Any]) -> dict[str, Any]:
    return {"notes": ["lazy-docker-python provides read-only container monitoring"]}


@server.handle_context(CAPABILITY["runtime_status"])
def runtime_status(params: dict[str, Any], context: RequestContext) -> dict[str, Any]:
    engine = select_engine(str(params.get("engine", "auto")))
    return {"engine": engine, "containers": normalized_containers(engine), "trace_id": context.trace_id}


@server.handle_context(CAPABILITY["runtime_logs"])
def runtime_logs(params: dict[str, Any], context: RequestContext) -> dict[str, Any]:
    name = params.get("name")
    if not isinstance(name, str) or not name or "\x00" in name:
        raise RPCError(400, "name is required")
    tail = params.get("tail", 50)
    if not isinstance(tail, int) or tail < 1 or tail > 500:
        raise RPCError(400, "tail must be between 1 and 500")
    engine = select_engine(str(params.get("engine", "auto")))
    completed = subprocess.run(
        [engine, "logs", "--tail", str(tail), name],
        check=False,
        capture_output=True,
        text=True,
        timeout=10,
    )
    if completed.returncode != 0:
        raise RPCError(502, completed.stderr.strip() or "container logs failed")
    return {"engine": engine, "name": name, "lines": completed.stdout.splitlines(), "operation_id": context.operation_id}


if __name__ == "__main__":
    server.serve(sys.stdin.buffer, sys.stdout.buffer)

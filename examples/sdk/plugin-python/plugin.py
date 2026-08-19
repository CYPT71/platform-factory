#!/usr/bin/env python3
"""Reference secure-oci v1 language plugin, built on the secure_oci_plugin SDK.

This is the Python counterpart of examples/sdk/plugin-go: where the Go
example implements sdk/plugin.LanguageExtension and lets
sdk/plugin.Runtime handle framing, handshake and dispatch, this example
imports sdk/plugin-python's Server and does the same. Both speak the
exact same wire protocol and pass the exact same conformance suite
(secure-oci-conformance plugin).
"""
import os
import sys

# In a real deployment, install the SDK (`pip install secure-oci-plugin`)
# instead of this path shim; it exists only so this example runs directly
# from a source checkout with no build step.
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "..", "sdk", "plugin-python"))

from secure_oci_plugin import Server  # noqa: E402

server = Server("python-example", "1.0.0")


@server.handle("detect")
def detect(params):
    return {"kind": "python", "profile": "python", "evidence": ["python-example"]}


@server.handle("freeze")
def freeze(params):
    return {"steps": [["python", "-m", "pip", "freeze"]], "profile": "python"}


@server.handle("plan")
def plan(params):
    return {"notes": ["Python example extension selected"]}


if __name__ == "__main__":
    server.serve(sys.stdin.buffer, sys.stdout.buffer)

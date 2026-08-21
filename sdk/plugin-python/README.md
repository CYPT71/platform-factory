# platform-factory

The Python SDK for out-of-process secure-oci language plugins. It defines
the versioned, length-prefixed JSON-RPC protocol plugins speak over
stdin/stdout (an LSP/DAP-style header-framed message on the wire) and the
plugin-side `Server`. This mirrors Go's `sdk/plugin` package field for
field; a plugin written against either SDK speaks the exact same wire
protocol and passes the exact same conformance suite
(`platform-factory plugin`).

Standard library only - no third-party dependencies.

## Install

Locally, during development (editable install from a source checkout):

```sh
pip install -e path/to/platform-factory/sdk/plugin-python
```

## Usage

```python
from secure_oci_plugin import Server

server = Server("my-language", "1.0.0")

@server.handle("detect")
def detect(params):
    return {"kind": "my-language", "profile": "static"}

@server.handle("freeze")
def freeze(params):
    return {"steps": [["my-package-manager", "freeze"]], "profile": "static"}

@server.handle("plan")
def plan(params):
    return {"notes": ["my-language extension selected"]}

if __name__ == "__main__":
    import sys
    server.serve(sys.stdin.buffer, sys.stdout.buffer)
```

See `examples/sdk/plugin-python` for the complete, runnable example.

For arbitrary capabilities, `handle_context` supplies native dictionaries and
an immutable `RequestContext`; `CAPABILITY` provides standard names. The SDK
owns framing and TraceID/OperationID correlation.

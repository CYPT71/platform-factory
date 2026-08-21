"""SDK for out-of-process secure-oci language plugins.

Defines the versioned, length-prefixed JSON-RPC protocol plugins speak
over stdin/stdout (an LSP/DAP-style header-framed message on the wire)
and the plugin-side Server. Mirrors Go's sdk/plugin package: the same
wire protocol, the same v1.hello handshake, the same capability
dispatch (method "v1."+capability). A plugin written against either
SDK passes the exact same conformance suite
(platform-factory plugin).

Standard library only.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any, BinaryIO, Callable, Dict

CONTENT_TYPE = "application/vnd.platform-factory.rpc.v1+json"
# LEGACY_CONTENT_TYPE is the pre-rebrand Content-Type: still accepted from
# a peer for the documented compatibility overlap window (see
# docs/api-compatibility.md), never written by write_message.
LEGACY_CONTENT_TYPE = "application/vnd.secure-oci.rpc.v1+json"
PROTOCOL_VERSION = "v1"
_MAX_MESSAGE_BYTES = 1 << 20

Handler = Callable[[Dict[str, Any]], Dict[str, Any]]
ContextHandler = Callable[[Dict[str, Any], "RequestContext"], Dict[str, Any]]

CAPABILITY = {
    "runtime_status": "runtime.status", "runtime_logs": "runtime.logs",
    "runtime_create": "runtime.create", "runtime_start": "runtime.start",
    "runtime_stop": "runtime.stop", "runtime_restart": "runtime.restart",
    "runtime_delete": "runtime.delete", "runtime_exec": "runtime.exec",
    "deployment_plan": "deployment.plan", "deployment_apply": "deployment.apply",
    "deployment_observe": "deployment.observe", "deployment_rollback": "deployment.rollback",
    "deployment_delete": "deployment.delete", "builder_build": "builder.build",
    "analyzer_scan": "analyzer.scan", "registry_push": "registry.push",
    "migration_discover": "migration.discover", "migration_apply": "migration.apply",
}

@dataclass(frozen=True)
class RequestContext:
    trace_id: str = ""
    operation_id: str = ""


class RPCError(Exception):
    """A protocol-level error to return to the host for one request.

    Raising any other exception from a handler is also accepted; it is
    reported with code 500 and the exception's message, matching what a
    handler that fails unexpectedly gets in the Go and JavaScript SDKs.
    """

    def __init__(self, code: int, message: str):
        super().__init__(message)
        self.code = code
        self.message = message


class ProtocolError(Exception):
    """A framing or transport-level error: malformed headers, an
    unsupported Content-Type, a truncated body, or a body over the
    1 MiB limit."""


def write_message(output: BinaryIO, value: Dict[str, Any]) -> None:
    """Frame value as a Content-Type/Content-Length-prefixed JSON
    message and write it to output, matching Go's WriteMessage and
    JavaScript's writeMessage byte for byte."""
    body = json.dumps(value, separators=(",", ":")).encode("utf-8")
    if len(body) > _MAX_MESSAGE_BYTES:
        raise ProtocolError(f"message of {len(body)} bytes exceeds the {_MAX_MESSAGE_BYTES} byte limit")
    header = f"Content-Type: {CONTENT_TYPE}\r\nContent-Length: {len(body)}\r\n\r\n"
    output.write(header.encode("ascii"))
    output.write(body)
    output.flush()


def read_message(input_stream: BinaryIO) -> Dict[str, Any]:
    """Read one framed message from input_stream and return its
    decoded JSON body. Returns None at a clean EOF between messages;
    raises ProtocolError for a truncated header, a missing or invalid
    Content-Length, or an unsupported Content-Type."""
    headers: Dict[str, str] = {}
    while True:
        line = input_stream.readline()
        if not line:
            if headers:
                raise ProtocolError("truncated header")
            return None  # type: ignore[return-value]
        line = line.rstrip(b"\r\n")
        if line == b"":
            break
        if b":" not in line:
            raise ProtocolError(f"malformed header {line!r}")
        key, _, value = line.partition(b":")
        headers[key.decode("ascii").strip().lower()] = value.decode("ascii").strip()

    content_type = headers.get("content-type")
    if content_type != CONTENT_TYPE and content_type != LEGACY_CONTENT_TYPE:
        raise ProtocolError(f"unsupported Content-Type {content_type!r}, want {CONTENT_TYPE!r}")
    raw_length = headers.get("content-length")
    if raw_length is None:
        raise ProtocolError("missing Content-Length header")
    try:
        length = int(raw_length)
    except ValueError:
        length = -1
    if length < 0 or length > _MAX_MESSAGE_BYTES:
        raise ProtocolError(f"invalid Content-Length {raw_length!r}")

    body = input_stream.read(length)
    if len(body) != length:
        raise ProtocolError("truncated body")
    return json.loads(body)


class Server:
    """The plugin-side SDK: register capabilities, then serve."""

    def __init__(self, name: str, version: str):
        if not name or not version:
            raise ValueError("secure_oci_plugin: Server requires a name and a version")
        self._name = name
        self._version = version
        self._capabilities: list[str] = []
        self._handlers: Dict[str, Handler] = {}
        self._context_handlers: Dict[str, ContextHandler] = {}

    def handle(self, capability: str, handler: Handler | None = None):
        """Register handler for capability (e.g. "detect"), dispatched
        on method "v1."+capability and advertised in the v1.hello
        response. Usable as a plain call (server.handle("detect", fn))
        or as a decorator (@server.handle("detect"))."""
        if handler is not None:
            self._capabilities.append(capability)
            self._handlers["v1." + capability] = handler
            return handler

        def decorator(fn: Handler) -> Handler:
            self._capabilities.append(capability)
            self._handlers["v1." + capability] = fn
            return fn

        return decorator

    def handle_context(self, capability: str, handler: ContextHandler | None = None):
        """Register a native handler receiving params and RequestContext."""
        def register(fn: ContextHandler) -> ContextHandler:
            self._capabilities.append(capability)
            self._context_handlers["v1." + capability] = fn
            return fn
        return register(handler) if handler is not None else register

    def serve(self, input_stream: BinaryIO, output: BinaryIO) -> None:
        """Read framed requests from input_stream and write framed
        responses to output until input_stream is exhausted (the host
        closed the connection)."""
        while True:
            request = read_message(input_stream)
            if request is None:
                return
            write_message(output, self._dispatch(request))

    def _dispatch(self, request: Dict[str, Any]) -> Dict[str, Any]:
        request_id = request.get("id", "")
        method = request.get("method")
        params = request.get("params") or {}
        correlation = {"trace_id": request.get("trace_id", ""),
                       "operation_id": request.get("operation_id", "")}

        if method == "v1.hello":
            return {
                "id": request_id,
                "result": {
                    "api_version": PROTOCOL_VERSION,
                    "name": self._name,
                    "version": self._version,
                    "capabilities": self._capabilities,
                }, **correlation,
            }

        handler = self._handlers.get(method)
        context_handler = self._context_handlers.get(method)
        if handler is None and context_handler is None:
            return {"id": request_id, "error": {"code": 404, "message": f"unknown method {method!r}"}, **correlation}
        try:
            context = RequestContext(str(request.get("trace_id", "")), str(request.get("operation_id", "")))
            result = context_handler(params, context) if context_handler is not None else handler(params)
        except RPCError as exc:
            return {"id": request_id, "error": {"code": exc.code, "message": exc.message}, **correlation}
        except Exception as exc:  # noqa: BLE001 - reported to the host, not swallowed
            return {"id": request_id, "error": {"code": 500, "message": str(exc)}, **correlation}
        return {"id": request_id, "result": result, **correlation}


__all__ = ["Server", "RequestContext", "CAPABILITY", "RPCError", "ProtocolError", "CONTENT_TYPE", "LEGACY_CONTENT_TYPE", "PROTOCOL_VERSION", "write_message", "read_message"]

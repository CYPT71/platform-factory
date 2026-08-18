"use strict";

// SDK for out-of-process secure-oci language plugins. Defines the
// versioned, length-prefixed JSON-RPC protocol plugins speak over
// stdin/stdout (an LSP/DAP-style header-framed message on the wire) and
// the plugin-side Server. Mirrors Go's sdk/plugin package: the same wire
// protocol, the same v1.hello handshake, the same capability dispatch
// (method "v1."+capability). A plugin written against either SDK passes
// the exact same conformance suite (secure-oci-conformance plugin).
//
// No third-party dependencies.

const CONTENT_TYPE = "application/vnd.platform-factory.rpc.v1+json";
// LEGACY_CONTENT_TYPE is the pre-rebrand Content-Type: still accepted
// from a peer for the documented compatibility overlap window (see
// docs/api-compatibility.md), never written by writeMessage.
const LEGACY_CONTENT_TYPE = "application/vnd.secure-oci.rpc.v1+json";
const PROTOCOL_VERSION = "v1";
const MAX_MESSAGE_BYTES = 1 << 20;
const CAPABILITY = Object.freeze({
  runtimeStatus: "runtime.status", runtimeLogs: "runtime.logs", runtimeCreate: "runtime.create",
  runtimeStart: "runtime.start", runtimeStop: "runtime.stop", runtimeRestart: "runtime.restart",
  runtimeDelete: "runtime.delete", runtimeExec: "runtime.exec", deploymentPlan: "deployment.plan",
  deploymentApply: "deployment.apply", deploymentObserve: "deployment.observe",
  deploymentRollback: "deployment.rollback", deploymentDelete: "deployment.delete",
  builderBuild: "builder.build", analyzerScan: "analyzer.scan", registryPush: "registry.push",
  migrationDiscover: "migration.discover", migrationApply: "migration.apply",
});

/** RPCError is a protocol-level error a handler can throw to control the
 * exact code/message sent back to the host, instead of the generic 500
 * a plain Error produces. */
class RPCError extends Error {
  constructor(code, message) {
    super(message);
    this.code = code;
  }
}

/** writeMessage frames value as a Content-Type/Content-Length-prefixed
 * JSON message and writes it to output, matching Go's WriteMessage and
 * Python's write_message byte for byte. */
function writeMessage(output, value) {
  const body = Buffer.from(JSON.stringify(value), "utf8");
  if (body.length > MAX_MESSAGE_BYTES) {
    throw new Error(`plugin: message of ${body.length} bytes exceeds the ${MAX_MESSAGE_BYTES} byte limit`);
  }
  output.write(`Content-Type: ${CONTENT_TYPE}\r\nContent-Length: ${body.length}\r\n\r\n`, "ascii");
  output.write(body);
}

/** Server is the plugin-side SDK: register capabilities, then serve. */
class Server {
  constructor(name, version) {
    if (!name || !version) {
      throw new Error("secure-oci plugin sdk: Server requires a name and a version");
    }
    this._name = name;
    this._version = version;
    this._capabilities = [];
    this._handlers = new Map();
    this._contextHandlers = new Map();
  }

  handleContext(capability, handler) {
    this._capabilities.push(capability);
    this._contextHandlers.set("v1." + capability, handler);
    return this;
  }

  /** handle registers handler for capability (e.g. "detect"), dispatched
   * on method "v1."+capability and advertised in the v1.hello response.
   * handler receives the request's params object and returns a plain
   * object result (synchronously or via a Promise). */
  handle(capability, handler) {
    this._capabilities.push(capability);
    this._handlers.set("v1." + capability, handler);
    return this;
  }

  /** serve reads framed requests from input and writes framed responses
   * to output until input ends (the host closed the connection). Returns
   * a Promise that resolves on clean shutdown. */
  serve(input, output) {
    return new Promise((resolve, reject) => {
      let buffer = Buffer.alloc(0);
      let closed = false;

      const finish = (err) => {
        if (closed) return;
        closed = true;
        input.removeListener("data", onData);
        input.removeListener("end", onEnd);
        input.removeListener("error", onError);
        if (err) reject(err);
        else resolve();
      };
      const onError = (err) => finish(err);
      const onEnd = () => finish();

      const onData = (chunk) => {
        buffer = Buffer.concat([buffer, chunk]);
        for (;;) {
          const headerEnd = buffer.indexOf("\r\n\r\n");
          if (headerEnd < 0) return;
          let headers;
          try {
            headers = parseHeaders(buffer.subarray(0, headerEnd));
          } catch (err) {
            finish(err);
            return;
          }
          const length = Number(headers["content-length"]);
          if (headers["content-type"] !== CONTENT_TYPE && headers["content-type"] !== LEGACY_CONTENT_TYPE) {
            finish(new Error(`plugin: unsupported Content-Type ${JSON.stringify(headers["content-type"])}, want ${JSON.stringify(CONTENT_TYPE)}`));
            return;
          }
          if (!Number.isInteger(length) || length < 0 || length > MAX_MESSAGE_BYTES) {
            finish(new Error(`plugin: invalid Content-Length ${JSON.stringify(headers["content-length"])}`));
            return;
          }
          const bodyStart = headerEnd + 4;
          if (buffer.length < bodyStart + length) return;
          const request = JSON.parse(buffer.subarray(bodyStart, bodyStart + length).toString("utf8"));
          buffer = buffer.subarray(bodyStart + length);
          Promise.resolve(this._dispatch(request))
            .then((response) => writeMessage(output, response))
            .catch((err) => finish(err));
        }
      };

      input.on("data", onData);
      input.on("end", onEnd);
      input.on("error", onError);
    });
  }

  async _dispatch(request) {
    const id = request.id || "";
    const method = request.method;

    if (method === "v1.hello") {
      return {
        id,
        result: {
          api_version: PROTOCOL_VERSION,
          name: this._name,
          version: this._version,
          capabilities: this._capabilities,
        }, trace_id: request.trace_id || "", operation_id: request.operation_id || "",
      };
    }

    const handler = this._handlers.get(method);
    const contextHandler = this._contextHandlers.get(method);
    if (!handler && !contextHandler) {
      return { id, error: { code: 404, message: `unknown method ${JSON.stringify(method)}` }, trace_id: request.trace_id || "", operation_id: request.operation_id || "" };
    }
    try {
      const context = Object.freeze({ traceId: request.trace_id || "", operationId: request.operation_id || "" });
      const result = await (contextHandler || handler)(request.params || {}, context);
      return { id, result, trace_id: context.traceId, operation_id: context.operationId };
    } catch (err) {
      if (err instanceof RPCError) {
        return { id, error: { code: err.code, message: err.message }, trace_id: request.trace_id || "", operation_id: request.operation_id || "" };
      }
      return { id, error: { code: 500, message: String((err && err.message) || err) }, trace_id: request.trace_id || "", operation_id: request.operation_id || "" };
    }
  }
}

function parseHeaders(raw) {
  const headers = {};
  for (const line of raw.toString("ascii").split("\r\n")) {
    const separator = line.indexOf(":");
    if (separator < 0) {
      throw new Error(`plugin: malformed header ${JSON.stringify(line)}`);
    }
    headers[line.slice(0, separator).trim().toLowerCase()] = line.slice(separator + 1).trim();
  }
  return headers;
}

module.exports = { Server, RPCError, CAPABILITY, writeMessage, CONTENT_TYPE, LEGACY_CONTENT_TYPE, PROTOCOL_VERSION };

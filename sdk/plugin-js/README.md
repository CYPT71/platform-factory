# @platform-factory/plugin-sdk

The JavaScript/TypeScript SDK for out-of-process platform-factory language
plugins. It defines the versioned, length-prefixed JSON-RPC protocol
plugins speak over stdin/stdout (an LSP/DAP-style header-framed message on
the wire) and the plugin-side `Server`. This mirrors Go's `sdk/plugin`
package field for field; a plugin written against either SDK speaks the
exact same wire protocol and passes the exact same conformance suite
(`platform-factory-conformance plugin`).

No third-party dependencies. CommonJS, with `index.d.ts` for TypeScript.

## Usage

```js
const { Server } = require("@platform-factory/plugin-sdk");

const server = new Server("my-language", "1.0.0");

server.handle("detect", () => ({ kind: "my-language", profile: "static" }));
server.handle("freeze", () => ({ steps: [["my-package-manager", "freeze"]], profile: "static" }));
server.handle("plan", () => ({ notes: ["my-language extension selected"] }));

server.serve(process.stdin, process.stdout).catch((err) => {
  console.error(err);
  process.exit(1);
});
```

See `examples/sdk/plugin-javascript` and `examples/sdk/plugin-typescript`
for complete, runnable examples.

`handleContext(CAPABILITY.runtimeStatus, handler)` supports arbitrary
capabilities with native values and immutable `traceId`/`operationId` fields.

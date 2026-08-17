#!/usr/bin/env node
"use strict";

// Reference platform-factory v1 language plugin, built on the
// @platform-factory/plugin-sdk package. This is the JavaScript counterpart of
// examples/sdk/plugin-go: where the Go example implements
// sdk/plugin.LanguageExtension and lets sdk/plugin.Runtime handle
// framing, handshake and dispatch, this example imports
// sdk/plugin-js's Server and does the same. Both speak the exact same
// wire protocol and pass the exact same conformance suite
// (platform-factory-conformance plugin).

// In a real deployment, install the SDK (`npm install @platform-factory/plugin-sdk`)
// instead of this relative path; it exists only so this example runs
// directly from a source checkout with no build step.
const { Server } = require("../../../sdk/plugin-js");

const server = new Server("javascript-example", "1.0.0");

server.handle("detect", () => ({ kind: "javascript", profile: "node", evidence: ["javascript-example"] }));
server.handle("freeze", () => ({ steps: [["npm", "ci", "--ignore-scripts"]], profile: "node" }));
server.handle("plan", () => ({ notes: ["JavaScript example extension selected"] }));

server.serve(process.stdin, process.stdout).catch((err) => {
  console.error(err);
  process.exit(1);
});

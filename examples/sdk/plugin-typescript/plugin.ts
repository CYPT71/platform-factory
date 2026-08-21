#!/usr/bin/env node
// Reference secure-oci v1 language plugin, built on the
// @secure-oci/plugin-sdk package's typed capability schemas. This is the
// TypeScript counterpart of examples/sdk/plugin-go: where the Go example
// implements sdk/plugin.LanguageExtension and lets sdk/plugin.Runtime
// handle framing, handshake and dispatch, this example imports
// sdk/plugin-js's Server (and its index.d.ts types) and does the same.
// Both speak the exact same wire protocol and pass the exact same
// conformance suite (platform-factory plugin).
//
// In a real deployment, install the SDK (`npm install @secure-oci/plugin-sdk`)
// instead of this relative path; it exists only so this example compiles
// directly from a source checkout with no publish step.
import { Server, DetectParams, DetectResult, FreezeParams, FreezeResult, PlanParams, PlanResult } from "../../../sdk/plugin-js";

const server = new Server("typescript-example", "1.0.0");

server.handle<DetectParams, DetectResult>("detect", () => ({
  kind: "typescript",
  profile: "node",
  evidence: ["typescript-example"],
}));

server.handle<FreezeParams, FreezeResult>("freeze", () => ({
  steps: [["npm", "ci", "--ignore-scripts"]],
  profile: "node",
}));

server.handle<PlanParams, PlanResult>("plan", () => ({
  notes: ["TypeScript example extension selected"],
}));

server.serve(process.stdin, process.stdout).catch((err: unknown) => {
  console.error(err);
  process.exit(1);
});

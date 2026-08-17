#!/usr/bin/env node
"use strict";
const { execFileSync } = require("node:child_process");
const { Server, RPCError, CAPABILITY } = require("../../../sdk/plugin-js");
const server = new Server("lazy-docker-javascript", "1.0.0");

function engine(requested = "auto") {
  if (!["auto", "docker", "podman"].includes(requested)) throw new RPCError(400, "invalid engine");
  for (const name of requested === "auto" ? ["docker", "podman"] : [requested]) {
    try { execFileSync(name, ["version"], { stdio: "ignore", timeout: 5000 }); return name; } catch (_) { /* try next */ }
  }
  throw new RPCError(503, "Docker or Podman is unavailable");
}
function containers(selected) {
  let output;
  try { output = execFileSync(selected, ["ps", "--all", "--format", "{{json .}}"], { encoding: "utf8", timeout: 10000 }); }
  catch (error) { throw new RPCError(502, `${selected} ps failed: ${error.message}`); }
  return output.trim().split("\n").filter(Boolean).map(JSON.parse).map((row) => ({
    id: row.ID || row.Id || "", name: row.Names || row.Name || "", image: row.Image || "",
    state: row.State || row.Status || "unknown", ports: row.Ports || "",
  })).sort((a, b) => a.name.localeCompare(b.name));
}
server.handle("detect", () => ({ kind: "unknown" }));
server.handle("freeze", () => ({ steps: [["node", "--version"]], profile: "runtime-monitor" }));
server.handle("plan", () => ({ notes: ["read-only Docker/Podman monitoring"] }));
server.handleContext(CAPABILITY.runtimeStatus, ({ engine: requested }, context) => { const selected = engine(requested); return { engine: selected, containers: containers(selected), trace_id: context.traceId }; });
server.handleContext(CAPABILITY.runtimeLogs, ({ engine: requested, name, tail = 50 }, context) => {
  if (typeof name !== "string" || !name || name.includes("\0")) throw new RPCError(400, "name is required");
  if (!Number.isInteger(tail) || tail < 1 || tail > 500) throw new RPCError(400, "tail must be between 1 and 500");
  const selected = engine(requested); const output = execFileSync(selected, ["logs", "--tail", String(tail), name], { encoding: "utf8", timeout: 10000 });
  return { engine: selected, name, lines: output.replace(/\n$/, "").split("\n"), operation_id: context.operationId };
});
server.serve(process.stdin, process.stdout).catch((error) => { console.error(error); process.exit(1); });

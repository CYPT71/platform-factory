#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { Server, RPCError, CAPABILITY, RequestContext } from "../../../sdk/plugin-js";

type StatusParams = { engine?: "auto" | "docker" | "podman" };
type LogsParams = StatusParams & { name: string; tail?: number };
type Container = { id: string; name: string; image: string; state: string; ports: string };
const server = new Server("lazy-docker-typescript", "1.0.0");

function selectEngine(requested: StatusParams["engine"] = "auto"): string {
  for (const name of requested === "auto" ? ["docker", "podman"] : [requested]) {
    try { execFileSync(name, ["version"], { stdio: "ignore", timeout: 5000 }); return name; } catch { /* try next */ }
  }
  throw new RPCError(503, "Docker or Podman is unavailable");
}
function containers(engine: string): Container[] {
  const output = execFileSync(engine, ["ps", "--all", "--format", "{{json .}}"], { encoding: "utf8", timeout: 10000 });
  return output.trim().split("\n").filter(Boolean).map((line) => JSON.parse(line) as Record<string, string>).map((row) => ({
    id: row.ID || row.Id || "", name: row.Names || row.Name || "", image: row.Image || "",
    state: row.State || row.Status || "unknown", ports: row.Ports || "",
  })).sort((a, b) => a.name.localeCompare(b.name));
}
server.handle("detect", () => ({ kind: "unknown" }));
server.handle("freeze", () => ({ steps: [["node", "--version"]], profile: "runtime-monitor" }));
server.handle("plan", () => ({ notes: ["read-only Docker/Podman monitoring"] }));
server.handleContext<StatusParams, { engine: string; containers: Container[]; trace_id: string }>(CAPABILITY.runtimeStatus, (params, context: RequestContext) => { const engine = selectEngine(params.engine); return { engine, containers: containers(engine), trace_id: context.traceId }; });
server.handleContext<LogsParams, { engine: string; name: string; lines: string[]; operation_id: string }>(CAPABILITY.runtimeLogs, (params, context: RequestContext) => {
  const tail = params.tail ?? 50; if (!params.name || params.name.includes("\0")) throw new RPCError(400, "name is required"); if (!Number.isInteger(tail) || tail < 1 || tail > 500) throw new RPCError(400, "invalid tail");
  const engine = selectEngine(params.engine); const output = execFileSync(engine, ["logs", "--tail", String(tail), params.name], { encoding: "utf8", timeout: 10000 }); return { engine, name: params.name, lines: output.replace(/\n$/, "").split("\n"), operation_id: context.operationId };
});
server.serve(process.stdin, process.stdout).catch((error: unknown) => { console.error(error); process.exit(1); });

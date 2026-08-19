// Type definitions for @secure-oci/plugin-sdk.
// Mirrors Go's sdk/plugin package's typed capability schemas
// (DetectParams/Result, FreezeParams/Result, PlanParams/Result), so a
// TypeScript plugin author gets the same compile-time contract a Go
// author gets from sdk/plugin.LanguageExtension.

import type { Readable, Writable } from "node:stream";

export const CONTENT_TYPE: string;
export const LEGACY_CONTENT_TYPE: string;
export const PROTOCOL_VERSION: string;
export const CAPABILITY: Readonly<Record<string, string>>;
export interface RequestContext { readonly traceId: string; readonly operationId: string; }

export class RPCError extends Error {
  code: number;
  constructor(code: number, message: string);
}

export function writeMessage(output: Writable, value: unknown): void;

export interface DetectParams {
  path: string;
}
export interface DetectResult {
  kind: string;
  profile?: string;
  evidence?: string[];
}

export interface FreezeParams {
  language: string;
  root: string;
}
export interface FreezeResult {
  steps: string[][];
  profile?: string;
}

export interface PlanParams {
  language: string;
  root: string;
}
export interface PlanResult {
  notes?: string[];
}

export type Handler<Params = unknown, Result = unknown> = (params: Params) => Result | Promise<Result>;

export class Server {
  constructor(name: string, version: string);
  handle<Params = unknown, Result = unknown>(capability: string, handler: Handler<Params, Result>): this;
  handleContext<Params = unknown, Result = unknown>(capability: string, handler: (params: Params, context: RequestContext) => Result | Promise<Result>): this;
  serve(input: Readable, output: Writable): Promise<void>;
}

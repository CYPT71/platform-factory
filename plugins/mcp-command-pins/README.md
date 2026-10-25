# mcp-command-pins

Reject unpinned npx MCP package launchers.

Offline JSON analyzer; no network, filesystem, secrets or model calls.
Shared implementation: `internal/extensions`; signed bundles: `pf extensions bundle`.

```sh
pf extensions inspect mcp-command-pins
pf extensions analyze mcp-command-pins --input document.json
```

RPC: `v1.analyzer.scan`, params: `{"document": <JSON object>}`.
The catalog example is the supported input contract. Findings are policy checks,
not verification of live infrastructure or cryptographic evidence.

Reference: https://blog.modelcontextprotocol.io/posts/mcp-roadmap/

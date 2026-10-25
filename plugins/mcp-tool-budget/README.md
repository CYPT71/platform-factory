# mcp-tool-budget

Bound exposed toolset size to reduce context cost.

Offline JSON analyzer; no network, filesystem, secrets or model calls.
Shared implementation: `internal/extensions`; signed bundles: `pf extensions bundle`.

```sh
pf extensions inspect mcp-tool-budget
pf extensions analyze mcp-tool-budget --input document.json
```

RPC: `v1.analyzer.scan`, params: `{"document": <JSON object>}`.
The catalog example is the supported input contract. Findings are policy checks,
not verification of live infrastructure or cryptographic evidence.

Reference: https://blog.modelcontextprotocol.io/posts/mcp-roadmap/

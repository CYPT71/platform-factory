# mcp-remote-tls

Require HTTPS endpoints in remote MCP inventories.

Offline JSON analyzer; no network, filesystem, secrets or model calls.
Shared implementation: `internal/extensions`; signed bundles: `pf extensions bundle`.

```sh
pf extensions inspect mcp-remote-tls
pf extensions analyze mcp-remote-tls --input document.json
```

RPC: `v1.analyzer.scan`, params: `{"document": <JSON object>}`.
The catalog example is the supported input contract. Findings are policy checks,
not verification of live infrastructure or cryptographic evidence.

Reference: https://blog.modelcontextprotocol.io/posts/mcp-roadmap/

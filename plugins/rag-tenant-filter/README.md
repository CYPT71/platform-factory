# rag-tenant-filter

Require tenant isolation in normalized retrieval policies.

Offline JSON analyzer; no network, filesystem, secrets or model calls.
Shared implementation: `internal/extensions`; signed bundles: `pf extensions bundle`.

```sh
pf extensions inspect rag-tenant-filter
pf extensions analyze rag-tenant-filter --input document.json
```

RPC: `v1.analyzer.scan`, params: `{"document": <JSON object>}`.
The catalog example is the supported input contract. Findings are policy checks,
not verification of live infrastructure or cryptographic evidence.

Reference: https://www.postgresql.org/docs/current/ddl-rowsecurity.html

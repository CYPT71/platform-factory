# data-vector-index

Validate bounded HNSW construction parameters.

Offline JSON analyzer; no network, filesystem, secrets or model calls.
Shared implementation: `internal/extensions`; signed bundles: `pf extensions bundle`.

```sh
pf extensions inspect data-vector-index
pf extensions analyze data-vector-index --input document.json
```

RPC: `v1.analyzer.scan`, params: `{"document": <JSON object>}`.
The catalog example is the supported input contract. Findings are policy checks,
not verification of live infrastructure or cryptographic evidence.

Reference: https://github.com/pgvector/pgvector#hnsw

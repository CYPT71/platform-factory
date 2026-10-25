# rag-chunk-budget

Bound RAG chunks and overlap before ingestion.

Offline JSON analyzer; no network, filesystem, secrets or model calls.
Shared implementation: `internal/extensions`; signed bundles: `pf extensions bundle`.

```sh
pf extensions inspect rag-chunk-budget
pf extensions analyze rag-chunk-budget --input document.json
```

RPC: `v1.analyzer.scan`, params: `{"document": <JSON object>}`.
The catalog example is the supported input contract. Findings are policy checks,
not verification of live infrastructure or cryptographic evidence.

Reference: https://owasp.org/www-project-top-10-for-large-language-model-applications/

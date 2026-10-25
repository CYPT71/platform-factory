# data-postgres-tls

Require PostgreSQL certificate hostname verification.

Offline JSON analyzer; no network, filesystem, secrets or model calls.
Shared implementation: `internal/extensions`; signed bundles: `pf extensions bundle`.

```sh
pf extensions inspect data-postgres-tls
pf extensions analyze data-postgres-tls --input document.json
```

RPC: `v1.analyzer.scan`, params: `{"document": <JSON object>}`.
The catalog example is the supported input contract. Findings are policy checks,
not verification of live infrastructure or cryptographic evidence.

Reference: https://www.postgresql.org/docs/current/libpq-ssl.html

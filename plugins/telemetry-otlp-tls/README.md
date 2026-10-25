# telemetry-otlp-tls

Require encrypted verified OTLP export.

Offline JSON analyzer; no network, filesystem, secrets or model calls.
Shared implementation: `internal/extensions`; signed bundles: `pf extensions bundle`.

```sh
pf extensions inspect telemetry-otlp-tls
pf extensions analyze telemetry-otlp-tls --input document.json
```

RPC: `v1.analyzer.scan`, params: `{"document": <JSON object>}`.
The catalog example is the supported input contract. Findings are policy checks,
not verification of live infrastructure or cryptographic evidence.

Reference: https://opentelemetry.io/docs/specs/semconv/gen-ai/

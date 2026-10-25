# supply-provenance-slsa

Check SLSA provenance predicate and SHA-256 subjects.

Offline JSON analyzer; no network, filesystem, secrets or model calls.
Shared implementation: `internal/extensions`; signed bundles: `pf extensions bundle`.

```sh
pf extensions inspect supply-provenance-slsa
pf extensions analyze supply-provenance-slsa --input document.json
```

RPC: `v1.analyzer.scan`, params: `{"document": <JSON object>}`.
The catalog example is the supported input contract. Findings are policy checks,
not verification of live infrastructure or cryptographic evidence.

Reference: https://slsa.dev/spec/v1.2/provenance

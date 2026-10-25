# supply-signature-policy

Reject unsigned artifacts and unconstrained signers.

Offline JSON analyzer; no network, filesystem, secrets or model calls.
Shared implementation: `internal/extensions`; signed bundles: `pf extensions bundle`.

```sh
pf extensions inspect supply-signature-policy
pf extensions analyze supply-signature-policy --input document.json
```

RPC: `v1.analyzer.scan`, params: `{"document": <JSON object>}`.
The catalog example is the supported input contract. Findings are policy checks,
not verification of live infrastructure or cryptographic evidence.

Reference: https://github.com/opencontainers/distribution-spec/blob/main/spec.md

# supply-sbom-spdx

Check SPDX document identity and package inventory.

Offline JSON analyzer; no network, filesystem, secrets or model calls.
Shared implementation: `internal/extensions`; signed bundles: `pf extensions bundle`.

```sh
pf extensions inspect supply-sbom-spdx
pf extensions analyze supply-sbom-spdx --input document.json
```

RPC: `v1.analyzer.scan`, params: `{"document": <JSON object>}`.
The catalog example is the supported input contract. Findings are policy checks,
not verification of live infrastructure or cryptographic evidence.

Reference: https://github.com/opencontainers/distribution-spec/blob/main/spec.md

# gateway-auth-policy

Require audience validation and disabled anonymous inference.

Offline JSON analyzer; no network, filesystem, secrets or model calls.
Shared implementation: `internal/extensions`; signed bundles: `pf extensions bundle`.

```sh
pf extensions inspect gateway-auth-policy
pf extensions analyze gateway-auth-policy --input document.json
```

RPC: `v1.analyzer.scan`, params: `{"document": <JSON object>}`.
The catalog example is the supported input contract. Findings are policy checks,
not verification of live infrastructure or cryptographic evidence.

Reference: https://owasp.org/www-project-top-10-for-large-language-model-applications/

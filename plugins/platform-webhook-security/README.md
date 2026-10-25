# platform-webhook-security

Require webhook HMAC verification and replay protection.

Offline JSON analyzer; no network, filesystem, secrets or model calls.
Shared implementation: `internal/extensions`; signed bundles: `pf extensions bundle`.

```sh
pf extensions inspect platform-webhook-security
pf extensions analyze platform-webhook-security --input document.json
```

RPC: `v1.analyzer.scan`, params: `{"document": <JSON object>}`.
The catalog example is the supported input contract. Findings are policy checks,
not verification of live infrastructure or cryptographic evidence.

Reference: https://owasp.org/www-project-top-10-for-large-language-model-applications/

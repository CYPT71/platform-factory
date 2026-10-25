# k8s-health-probes

Require startup and readiness probes on service containers.

Offline JSON analyzer; no network, filesystem, secrets or model calls.
Shared implementation: `internal/extensions`; signed bundles: `pf extensions bundle`.

```sh
pf extensions inspect k8s-health-probes
pf extensions analyze k8s-health-probes --input document.json
```

RPC: `v1.analyzer.scan`, params: `{"document": <JSON object>}`.
The catalog example is the supported input contract. Findings are policy checks,
not verification of live infrastructure or cryptographic evidence.

Reference: https://kubernetes.io/blog/2026/04/22/kubernetes-v1-36-release/

# k8s-image-digests

Require digest-pinned images in Deployment containers.

Offline JSON analyzer; no network, filesystem, secrets or model calls.
Shared implementation: `internal/extensions`; signed bundles: `pf extensions bundle`.

```sh
pf extensions inspect k8s-image-digests
pf extensions analyze k8s-image-digests --input document.json
```

RPC: `v1.analyzer.scan`, params: `{"document": <JSON object>}`.
The catalog example is the supported input contract. Findings are policy checks,
not verification of live infrastructure or cryptographic evidence.

Reference: https://kubernetes.io/blog/2026/04/22/kubernetes-v1-36-release/

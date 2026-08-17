# Distributed pipeline leases

`platform-factory-worker` executes real pipeline stages when started with an
administrator-owned workspace root:

```sh
platform-factory-worker \
  -control-plane https://control.example:9443 \
  -cert worker.pem -key worker-key.pem -ca ca.pem \
  -execution-root /var/lib/platform-factory/work \
  -cache-dir /var/lib/platform-factory/cache
```

The `payload` submitted to `POST /lease/submit` is a JSON string containing a
versioned envelope. The pipeline is inline; the work directory is relative to
the configured root:

```json
{
  "api_version": "platform-factory.dev/worker-pipeline/v1",
  "workdir": "build-01",
  "blobs": [{
    "digest": "sha256:…",
    "size": 42,
    "target": "src/input.tar"
  }],
  "pipeline": {
    "api_version": "platform-factory.dev/v1",
    "name": "build",
    "stages": []
  }
}
```

Start the control plane with `-cas-dir` to expose the optional CAS hub on the
same mTLS listener. `PUT /cas/blobs/sha256:…` verifies an upload in temporary
storage before mutating the CAS; workers use `GET` and verify digest and size
again before materializing a target.

The worker rejects unknown envelope fields, unsupported versions, payloads
larger than 1 MiB, absolute/traversing paths, and any pre-existing workspace.
This prevents a retry from consuming stale or attacker-prepared local state.
Pipeline validation and context cancellation are provided by the same
scheduler used by `pf pipeline run`.

Only explicitly declared CAS blobs are fetched; arbitrary remote source URLs
are never resolved implicitly. Sandbox selection remains a deployment
responsibility. The former timed
simulator is available only with the explicit `-demo-simulate` flag and must
not be used for production workers.

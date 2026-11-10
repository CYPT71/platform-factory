# Run distributed pipeline leases

The control plane assigns leases; a worker decodes each lease's inline pipeline
and executes its stages. This guide uses a real Linux/amd64 worker, not the
`-demo-simulate` executor.

## Prerequisites

Install the distributed binaries through the [installer](distribution.md).
Prepare a server certificate/key, a worker certificate/key and their trusted CA
bundle. The server certificate must cover IP `127.0.0.1` for the URL below.
Client identity comes from the verified certificate's non-empty `CommonName`;
its Subject Organization must contain `worker`.

The lifecycle control routes currently require that worker role, including
submit and cancel. An independently defined `operator` role is not accepted.
These commands assume existing certificates; they do not create a test PKI.
Run them from the repository root on a trusted development host.

## Start the two processes

Create an administrator-controlled workspace root, then start the control plane
in terminal 1:

```sh
mkdir -p dist/distributed/work
platform-factory-control-plane \
  -listen 127.0.0.1:8443 \
  -cert server.pem -key server-key.pem -ca ca.pem \
  -state-file dist/distributed/control.json \
  -audit-file dist/distributed/audit.jsonl
```

In terminal 2, start the worker with the same CA:

```sh
platform-factory-worker \
  -control-plane https://127.0.0.1:8443 \
  -cert worker.pem -key worker-key.pem -ca ca.pem \
  -platform linux/amd64 -max-parallel 2 \
  -execution-root "$PWD/dist/distributed/work"
```

`-max-parallel` limits concurrent leases. Stages inside each lease currently run
with scheduler parallelism 1. Do not advertise capabilities the host cannot
provide; a capability string is a scheduling declaration, not proof of isolation.

## Submit and inspect real work

The [submission fixture](../examples/distributed/submit-lease.json) writes
`built` to `build-01/artifact` beneath the worker root:

```sh
curl --fail-with-body \
  --cert worker.pem --key worker-key.pem --cacert ca.pem \
  -H 'Content-Type: application/json' \
  --data-binary @examples/distributed/submit-lease.json \
  https://127.0.0.1:8443/lease/submit
```

The outer request's `payload` is a **JSON string**, containing a
`platform-factory.dev/worker-pipeline/v1` envelope. Its `pipeline` is an inline
`platform-factory.dev/v1` document with a real `build` stage. Keep these two
wire versions distinct; sending the envelope directly is not a submission.

Use the returned `lease_id` to query
`GET /lease/status?id=LEASE_ID` with the same mTLS options. After completion,
`dist/distributed/work/build-01/artifact` should contain `built`. Change the
inner `workdir` before submitting again: pre-existing workspaces are refused,
including directories left by an earlier successful run.

For cancellation and the real-cluster scenario, see the
[distributed example](../examples/distributed/README.md).

## Enable shared blob inputs

Add `-cas-dir dist/distributed/cas` to the control-plane command. Upload each
blob with the same mTLS options and its actual length before submitting a lease:
`PUT /cas/blobs/sha256:HEX_DIGEST`. The worker fetches descriptors from that
route and verifies content before materializing it. A wrong digest is refused.
Every client chaining to the configured CA can read/write this shared store;
do not use it as a private tenant boundary. Bound disk capacity operationally.

## Isolation, storage and recovery limits

| Boundary | Implemented behavior and limit |
| --- | --- |
| Payload | Strict decoding and a 1 MiB limit. Workspace/blob paths reject parent traversal before materialization. Directory and file operations use `os.Root` to reject symlink escapes; existing workspaces are refused. This protects materialization, not the stage process's host access. |
| Execution | Uses `executor.New`, without namespaces or chroot. `network: none` is accepted but does **not** block host networking in this executor; secrets and unsupported sandbox/resource policies are refused across all stages before creating a lease workspace, fetching CAS inputs or running any stage. Isolate the worker host separately. |
| CAS | `-cas-dir DIR` enables persistent shared blobs at `/cas/blobs/`; leave it unset to disable that route. GET/PUT require a verified mTLS client, and PUT checks digest and length (maximum 1 GiB). This is a shared CA trust domain, not per-tenant storage isolation or a disk quota. Worker `-cache-dir` alone does not enable the server route. |
| Snapshots | `-state-file` enables scheduler restart recovery; it is not high availability. |
| Audit | `-audit-file` enables a hash-chained lifecycle journal, checked before appending after restart. It is separate from the scheduler snapshot. |
| Tenant quota | `-tenant-max-parallel N` atomically limits open leases with a non-empty tenant. With `-state-file`, startup restores pending/assigned slots, even above a lowered limit; new submissions wait for the old work to drain. Untagged requests still bypass enforcement; tenant names are not certificate-bound authorization. |

## Troubleshooting

| Symptom | Check |
| --- | --- |
| TLS or unauthorized response | CA trust, server SAN, client CommonName and Organization `worker` |
| Lease stays pending | Worker registration, advertised platform/capabilities and free lease slots |
| Payload decoding fails | Outer `payload` must be a string containing the versioned envelope |
| Workspace creation fails | Writable root and a fresh inner `workdir`; retain old results before cleanup |
| HTTP 429 for a tenant | Wait for slots or review configured limits; do not bypass quotas by dropping tenant identity |
| CAS returns 404 | Enable `-cas-dir` on the control plane and confirm the exact digest was uploaded; disabled routes and absent blobs both return 404 |

Sources: [control-plane startup](../cmd/platform-factory-control-plane/main.go),
[routes](../cmd/platform-factory-control-plane/server.go),
[worker executor](../internal/app/workerpipeline/workerpipeline.go).

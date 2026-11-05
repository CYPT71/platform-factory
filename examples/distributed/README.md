# Distributed scheduler

This advanced example needs three certificate identities issued by one test
CA: a server certificate, a worker certificate whose Organization contains
`worker`, and the CA certificate. Do not reuse development keys in production.

For real inline-pipeline execution, follow the
[distributed worker guide](../../docs/distributed-builds.md). It starts both
processes with a real workspace root and submits `submit-lease.json`, which
runs `sh -c 'printf built > artifact'`. The worker must advertise `linux/amd64`
for that fixture; edit `required_platform` to match another actual worker.
Use a fresh inner `workdir` for each execution.

`register-worker.json` illustrates the registration shape; a running worker
registers itself. JSON identity fields never replace the verified certificate
CommonName. Submit, status and cancel requests also require a client
certificate whose Organization contains `worker`; the filename `operator.pem`
below does not grant a distinct operator role.

To stop pending or running work, post `cancel-lease.json` with an authenticated
client certificate:

```sh
curl --fail --cert operator.pem --key operator-key.pem --cacert ca.pem \
  -H 'content-type: application/json' \
  --data @examples/distributed/cancel-lease.json \
  https://127.0.0.1:8443/lease/cancel
```

The first response contains `"canceled": true`; replaying the same request is
safe and returns `"canceled": false`. The terminal state survives a control
plane restart. A worker executing that lease observes it remotely, cancels the
executor context, waits for the executor to stop, and does not report a late
completion.

The audit file is an append-only, hash-chained JSONL lifecycle record. It is
verified before the control plane appends after a restart, so modification,
reordering and incomplete writes fail closed.

Expected result: the worker registers as `linux/amd64`, runs at most two
leases concurrently and creates `build-01/artifact` containing `built`. The
submission does not require KVM or a CAS hub. Additional scheduling constraints
can be supplied explicitly; they are declarations, not hardware or isolation
attestations.

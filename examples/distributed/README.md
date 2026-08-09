# Distributed scheduler

This advanced example needs three certificate identities issued by one test
CA: a server certificate, a worker certificate whose Organization contains
`worker`, and the CA certificate. Do not reuse development keys in production.

Start the mutually authenticated control plane and a worker with real
capability, cache-locality and load declarations:

```sh
go run ./cmd/platform-factory-control-plane \
  -listen 127.0.0.1:8443 -cert server.pem -key server-key.pem -ca ca.pem \
  -state-file /tmp/platform-factory-control.json \
  -audit-file /tmp/platform-factory-audit.jsonl

go run ./cmd/platform-factory-worker \
  -control-plane https://127.0.0.1:8443 \
  -cert worker.pem -key worker-key.pem -ca ca.pem \
  -platform linux/amd64 -capabilities kvm,network \
  -cached-content sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  -max-parallel 2
```

`register-worker.json` and `submit-lease.json` show the strict wire payloads.
The worker identity is always taken from the verified certificate CommonName;
an ID in JSON is never trusted.

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
leases concurrently, rejects work requiring unknown capabilities, and prefers
the supplied cached digest when multiple leases are eligible.

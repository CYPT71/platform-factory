# Run a platform-factory MicroVM from Docker

Docker can select `platform-factory-runtime` per workload without replacing its
default `runc` runtime. This path currently requires Linux `amd64`, `/dev/kvm`,
a digest-pinned kernel and the project guest init.

The reproducible end-to-end example starts a dedicated Docker daemon with its
own socket and data root, imports an OCI layout, then proves
`run/ps/logs/inspect/stop/rm` against the real KVM guest:

```sh
scripts/microvm/test-docker-kvm.sh \
  /absolute/path/to/oci-layout IMAGE \
  /absolute/path/to/kernel /absolute/path/to/microvm-init
```

For a system daemon, install the runtime binary and add an opt-in entry to
`/etc/docker/daemon.json` while preserving every existing key:

```json
{
  "runtimes": {
    "platform-factory-runtime": {
      "path": "/usr/local/bin/platform-factory-runtime"
    }
  }
}
```

After a controlled daemon reload, use `docker run --runtime
platform-factory-runtime ...` with the same four `platform-factory.dev/kernel-*` and
`platform-factory.dev/init-*` annotations shown by the Podman example. A plain
`docker run IMAGE` remains an ordinary container.

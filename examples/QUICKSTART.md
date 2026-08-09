# First platform-factory image in ten minutes

This walkthrough needs only Go. Docker, Podman and KVM are optional because
layout construction and verification are native.

From the repository root, build the CLI and the sample service:

```sh
go build -o /tmp/platform-factory ./cmd/platform-factory
go build -o /tmp/example-service ./cmd/example-service
```

Create a hardened OCI Image Layout using the supplied runtime configuration:

```sh
/tmp/platform-factory build \
  --config examples/platform-factory.json \
  --output /tmp/example-layout \
  /tmp/example-service
```

Verify and inspect the result before giving it to any runtime:

```sh
/tmp/platform-factory verify /tmp/example-layout
/tmp/platform-factory inspect /tmp/example-layout
```

You now have a content-addressed OCI layout under `/tmp/example-layout`.
Nothing was sent to a registry and no container daemon was started.

Next, choose a path from [`README.md`](README.md): run the DAG example to
learn builds and caching, use the MicroVM guide for KVM/HVF isolation, or use
the supply-chain guide before publishing an immutable digest.


# Run the same image as a container or a MicroVM with Podman

This example is for a Linux `amd64` host with `/dev/kvm`. Installing the
runtime adds an opt-in Podman runtime; it does not replace Podman's default
OCI runtime.

Build and install the runtime for the current user:

```sh
scripts/microvm/install-podman-runtime.sh
podman info --format '{{json .Host.OCIRuntime}}'
```

Prepare an image using either an existing registry reference or a local OCI
layout. For the repository example layout:

```sh
go build -o /tmp/platform-factory ./cmd/platform-factory
go build -o /tmp/example-service ./cmd/example-service
/tmp/platform-factory build --config examples/platform-factory.json \
  --output /tmp/example-layout /tmp/example-service
tar -C /tmp/example-layout -cf /tmp/example-layout.tar .
podman load --input /tmp/example-layout.tar
```

The native runtime also needs a readable, digest-pinned kernel and the project
guest init. Build the init and provide the kernel owned by your host setup:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -o /tmp/microvm-init ./cmd/microvm-init
KERNEL=/absolute/path/to/kernel
KERNEL_DIGEST=sha256:$(sha256sum "$KERNEL" | awk '{print $1}')
INIT_DIGEST=sha256:$(sha256sum /tmp/microvm-init | awk '{print $1}')
IMAGE=localhost/platform-factory:latest
```

Keep both execution modes available for the same image:

```sh
# Ordinary container: Podman's default runtime remains unchanged.
podman run --rm "$IMAGE"

# Isolated guest: this name and PID belong to a real platform-factory MicroVM.
podman run --detach --runtime platform-factory-runtime --name secure-img \
  --network none --cap-drop all \
  --annotation "platform-factory.dev/kernel-path=$KERNEL" \
  --annotation "platform-factory.dev/kernel-digest=$KERNEL_DIGEST" \
  --annotation "platform-factory.dev/init-path=/tmp/microvm-init" \
  --annotation "platform-factory.dev/init-digest=$INIT_DIGEST" \
  "$IMAGE"
```

Administer the MicroVM through Podman, not through a side-channel:

```sh
podman ps --all --filter name=secure-img
podman inspect secure-img
podman logs secure-img
podman stop secure-img
podman inspect --format '{{.State.Status}} {{.State.ExitCode}}' secure-img
podman rm secure-img
```

Expected result: `secure-img` becomes `exited` after `stop`, reports the
guest process exit status, and disappears after `rm`. There is no proxy or
shadow container. If KVM, the pinned kernel, the guest init or an OCI feature
is unsupported, creation fails closed instead of silently using the default
container runtime.

For a fully automated hardware proof using a freshly built layout, run
`scripts/microvm/test-podman-kvm.sh` as documented by that script.

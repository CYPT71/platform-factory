# MicroVM user paths

Start with the capability probe:

```sh
go build -o /tmp/platform-factory ./cmd/platform-factory
/tmp/platform-factory microvm probe
```

Stop when `available` is false. Linux needs `/dev/kvm`; Apple Silicon needs
the macOS virtualization entitlement.

## Native boot without OCI

This path boots a digest-pinned Linux kernel and initramfs directly through
KVM on Linux or Virtualization.framework/HVF on macOS. It does not invoke
QEMU, Firecracker, Cloud Hypervisor, libkrun or Kata.

```sh
KERNEL=/absolute/path/to/kernel
INITRAMFS=/absolute/path/to/initramfs
KERNEL_DIGEST=sha256:REPLACE_WITH_KERNEL_SHA256
INITRAMFS_DIGEST=sha256:REPLACE_WITH_INITRAMFS_SHA256

/tmp/platform-factory microvm run \
  --kernel "$KERNEL" --kernel-digest "$KERNEL_DIGEST" \
  --initramfs "$INITRAMFS" --initramfs-digest "$INITRAMFS_DIGEST" \
  --memory-mib 256 --vcpus 1
```

Digests are mandatory. Generate them with a trusted local hashing tool and
keep the `sha256:` prefix.

## OCI through an opt-in MicroVM runtime

On a configured Linux/KVM host, Podman retains both modes:

```sh
podman run --rm IMAGE
podman run --runtime platform-factory-runtime --name secure-img IMAGE
podman ps --all --filter name=secure-img
podman logs secure-img
podman inspect secure-img
podman stop secure-img
podman rm secure-img
```

The first command is a normal container. Only the second selects the
platform-factory MicroVM runtime. Installation and KVM prerequisites are documented
step by step in [`podman-microvm`](../podman-microvm) and exercised by
`scripts/microvm/test-podman-kvm.sh`.

For containerd/Kubernetes, continue with
[`containerd-kubernetes`](../containerd-kubernetes). A native OCI lifecycle on
macOS is currently proven by the signed HVF test harness, not exposed as a
general Podman runtime.

## Historical layout command

`platform-factory microvm run/start --layout ...` still delegates to
`scripts/microvm/run-microvm.sh`, which uses QEMU. It is retained for
compatibility and is not evidence of the native KVM/HVF architecture.

# Sourced (not executed) by scripts/microvm/*.sh. Maps `uname -m` to this
# project's amd64/arm64 convention and the matching QEMU binary. KVM only
# accelerates a guest matching the host architecture, so every script that
# sources this always targets the host's own architecture.
case "$(uname -m)" in
  x86_64)
    HOST_ARCH=amd64
    QEMU_BIN=qemu-system-x86_64
    ;;
  aarch64|arm64)
    HOST_ARCH=arm64
    QEMU_BIN=qemu-system-aarch64
    ;;
  *)
    HOST_ARCH=""
    QEMU_BIN=""
    ;;
esac

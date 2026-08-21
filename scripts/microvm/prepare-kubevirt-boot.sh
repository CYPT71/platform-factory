#!/usr/bin/env bash
# Produce a minimal Docker/Podman build context for KubeVirt external
# kernel boot. The resulting scratch image contains only the verified
# kernel and initramfs, owned by KubeVirt's qemu UID/GID (107).
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 OCI_IMAGE_DIR OUTPUT_CONTEXT" >&2
  exit 2
fi
image_dir=$1
output=$2

if [ -e "$output" ]; then
  echo "error: output already exists: $output" >&2
  exit 1
fi

if [ -n "${BASH_VERSION:-}" ]; then
  script_path=${BASH_SOURCE[0]}
elif [ -n "${ZSH_VERSION:-}" ]; then
  script_path=${(%):-%x}
else
  echo "error: this script requires Bash or Zsh" >&2
  exit 1
fi

repo_root=$(cd "$(dirname "$script_path")/../.." && pwd)
script_dir="$repo_root/scripts/microvm"
# shellcheck source=scripts/microvm/lib-arch.sh
. "$script_dir/lib-arch.sh"

image_arch=$HOST_ARCH

parent=$(dirname "$output")
mkdir -p "$parent"
context=$(mktemp -d "$parent/.platform-factory-kubevirt-boot.XXXXXX")
work=$(mktemp -d "${TMPDIR:-/tmp}/platform-factory-kubevirt-boot.XXXXXX")
cleanup() {
  rm -rf "$work"
  [ -d "$context" ] && rm -rf "$context"
}
trap cleanup EXIT

CGO_ENABLED=0 GOOS=linux GOARCH="$HOST_ARCH" go build -trimpath -ldflags='-s -w' \
  -o "$work/init" "$repo_root/cmd/microvm-init"
if command -v microvm-initramfs >/dev/null 2>&1; then
  initramfs_tool=$(command -v microvm-initramfs)
else
  initramfs_tool="$work/microvm-initramfs"
  go build -trimpath -ldflags='-s -w' -o "$initramfs_tool" "$repo_root/cmd/microvm-initramfs"
fi
"$initramfs_tool" -layout "$image_dir" -platform "linux/$HOST_ARCH" \
  -init "$work/init" -output "$work/initramfs.cpio.gz" > "$work/initramfs-result.json"

kernel="$repo_root/.cache/microvm/$HOST_ARCH/kernel"
"$script_dir/build-kernel.sh" "$HOST_ARCH" "$kernel"
mkdir -p "$context/boot"
cp "$kernel" "$context/boot/kernel"
cp "$work/initramfs.cpio.gz" "$context/boot/initramfs.cpio.gz"
chmod 0444 "$context/boot/kernel" "$context/boot/initramfs.cpio.gz"

cat > "$context/Dockerfile" <<'EOF'
FROM scratch
COPY --chown=107:107 boot/kernel /boot/kernel
COPY --chown=107:107 boot/initramfs.cpio.gz /boot/initramfs.cpio.gz
EOF

kernel_sha=$(sha256sum "$context/boot/kernel" | cut -d' ' -f1)
initramfs_sha=$(sha256sum "$context/boot/initramfs.cpio.gz" | cut -d' ' -f1)
python3 -c '
import json, sys
json.dump({
    "schema_version": 1,
    "architecture": sys.argv[1],
    "kernel_path": "/boot/kernel",
    "kernel_sha256": sys.argv[2],
    "initrd_path": "/boot/initramfs.cpio.gz",
    "initrd_sha256": sys.argv[3],
    "process_contract": "embedded:/etc/platform-factory/process.json",
}, open(sys.argv[4], "w"), indent=2)
' "$image_arch" "$kernel_sha" "$initramfs_sha" "$context/boot-metadata.json"

mv "$context" "$output"
context=""
echo "KUBEVIRT_BOOT_CONTEXT_READY path=$output architecture=$image_arch kernel_sha256=$kernel_sha initrd_sha256=$initramfs_sha"

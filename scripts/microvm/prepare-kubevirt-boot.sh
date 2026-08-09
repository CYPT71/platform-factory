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

python3 "$repo_root/scripts/ci/verify-oci-layout.py" "$image_dir"
image_meta=$(python3 -c '
import json, sys
root = sys.argv[1]
index = json.load(open(f"{root}/index.json"))
manifest_digest = index["manifests"][0]["digest"].split(":", 1)[1]
manifest = json.load(open(f"{root}/blobs/sha256/{manifest_digest}"))
config_digest = manifest["config"]["digest"].split(":", 1)[1]
config = json.load(open(f"{root}/blobs/sha256/{config_digest}"))
print(config["architecture"])
print(manifest["layers"][0]["digest"].split(":", 1)[1])
for value in config["config"]["Entrypoint"]:
    print(value)
' "$image_dir")

image_arch=$(sed -n '1p' <<<"$image_meta")
layer_digest=$(sed -n '2p' <<<"$image_meta")
entrypoint=()
while IFS= read -r value; do entrypoint+=("$value"); done < <(sed -n '3,$p' <<<"$image_meta")
if [ "$image_arch" != "$HOST_ARCH" ]; then
  echo "error: image architecture is $image_arch but build host is $HOST_ARCH" >&2
  exit 1
fi

parent=$(dirname "$output")
mkdir -p "$parent"
context=$(mktemp -d "$parent/.secure-oci-kubevirt-boot.XXXXXX")
work=$(mktemp -d "${TMPDIR:-/tmp}/secure-oci-kubevirt-boot.XXXXXX")
cleanup() {
  rm -rf "$work"
  [ -d "$context" ] && rm -rf "$context"
}
trap cleanup EXIT

CGO_ENABLED=0 GOOS=linux GOARCH="$HOST_ARCH" go build -trimpath -ldflags='-s -w' \
  -o "$work/init" "$repo_root/cmd/microvm-init"
"$script_dir/assemble-initramfs.sh" \
  "$image_dir/blobs/sha256/$layer_digest" "$work/init" "$work/initramfs.cpio.gz" \
  "${entrypoint[@]}"

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
    "entrypoint": sys.argv[5:],
}, open(sys.argv[4], "w"), indent=2)
' "$image_arch" "$kernel_sha" "$initramfs_sha" "$context/boot-metadata.json" "${entrypoint[@]}"

mv "$context" "$output"
context=""
echo "KUBEVIRT_BOOT_CONTEXT_READY path=$output architecture=$image_arch kernel_sha256=$kernel_sha initrd_sha256=$initramfs_sha"

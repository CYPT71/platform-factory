#!/usr/bin/env bash
# Download, checksum-verify, and build a minimal Linux kernel for the
# scripts/microvm boot path (amd64 or arm64). Linux-only. Native builds remain
# the default for KVM; MICROVM_CROSS_COMPILE=1 explicitly permits CI to build
# a guest for another host, such as an arm64 kernel consumed by macOS/HVF.
set -euo pipefail

# On macOS, transparently re-run this script inside a Linux Podman container
# instead of failing outright at the Linux-only check below. This preserves
# the normal command:
#
#   scripts/microvm/build-kernel.sh arm64 /tmp/kernel
#
# PODMAN_KERNEL_BUILD prevents recursive container launches once we're
# already running inside the container it starts.
if [ "$(uname -s)" = "Darwin" ] &&
   [ "${PODMAN_KERNEL_BUILD:-0}" != "1" ]; then
  command -v podman >/dev/null 2>&1 || {
    echo "error: Podman is required to build the Linux kernel from macOS" >&2
    exit 1
  }

  podman machine inspect >/dev/null 2>&1 || {
    echo "error: no Podman machine is available; run: podman machine init" >&2
    exit 1
  }

  podman info >/dev/null 2>&1 || {
    echo "error: Podman is not running; run: podman machine start" >&2
    exit 1
  }

  if [ "$#" -ne 2 ]; then
    echo "usage: $0 <amd64|arm64> OUTPUT_KERNEL_IMAGE" >&2
    exit 2
  fi

  requested_arch=$1
  requested_output=$2

  case "$requested_arch" in
    arm64)
      container_platform=linux/arm64
      ;;
    amd64)
      container_platform=linux/amd64
      ;;
    *)
      echo "error: unsupported architecture '$requested_arch' (supported: amd64, arm64)" >&2
      exit 2
      ;;
  esac

  # pwd -P (not plain pwd) below: Podman's macOS VM shares /Users, /private
  # and /var/folders via virtiofs, but does not resolve symlinks in a
  # --volume source path against those shares - a bind mount through /tmp
  # (which is itself a symlink to /private/tmp on macOS) fails with
  # "statfs: no such file or directory" even though the equivalent
  # /private/tmp path mounts fine. Resolving every host path to its
  # physical, symlink-free form before it reaches `podman run` sidesteps
  # that entirely, for the repo checkout and for wherever the caller asked
  # the kernel to be written (commonly somewhere under /tmp, as this
  # script's own usage example shows).
  script_path=${BASH_SOURCE[0]}
  script_dir=$(cd "$(dirname "$script_path")" && pwd -P)
  repo_root=$(cd "$script_dir/../.." && pwd -P)

  case "$requested_output" in
    /*)
      host_output=$requested_output
      ;;
    *)
      host_output="$PWD/$requested_output"
      ;;
  esac

  output_dir=$(dirname "$host_output")
  output_name=$(basename "$host_output")

  mkdir -p "$output_dir"
  output_dir=$(cd "$output_dir" && pwd -P)
  host_output="$output_dir/$output_name"

  echo "kernel-build host=darwin executor=podman architecture=$requested_arch" >&2
  echo "kernel-build output=$host_output" >&2

  exec podman run --rm \
    --platform "$container_platform" \
    --volume "$repo_root:/workspace" \
    --volume "$output_dir:/kernel-output" \
    --workdir /workspace \
    --env PODMAN_KERNEL_BUILD=1 \
    --env FORCE_REBUILD="${FORCE_REBUILD:-0}" \
    docker.io/library/ubuntu:24.04 \
    bash -euo pipefail -c '
      export DEBIAN_FRONTEND=noninteractive

      apt-get update
      apt-get install -y --no-install-recommends \
        bc \
        bison \
        ca-certificates \
        curl \
        flex \
        gcc \
        libelf-dev \
        libssl-dev \
        make \
        python3 \
        xz-utils

      scripts/microvm/build-kernel.sh "$1" "/kernel-output/$2"
    ' bash "$requested_arch" "$output_name"
fi

log() {
  printf '[%s] kernel-build %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*" >&2
}

KERNEL_VERSION=6.12.98
KERNEL_SHA256=a62b6a2d207ff72510e5f47156b7078e1e71797357412411b8e4fff97fc8f4c7
KERNEL_URL="https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-${KERNEL_VERSION}.tar.xz"

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <amd64|arm64> OUTPUT_KERNEL_IMAGE" >&2
  exit 2
fi
arch=$1
output=$2

case "$arch" in
  amd64)
    kernel_arch=x86_64
    defconfig=arch/x86/configs/x86_64_defconfig
    make_target=bzImage
    built_image=arch/x86/boot/bzImage
    ;;
  arm64)
    kernel_arch=arm64
    defconfig=arch/arm64/configs/defconfig
    make_target=Image
    built_image=arch/arm64/boot/Image
    ;;
  *)
    echo "error: unsupported architecture '$arch' (supported: amd64, arm64)" >&2
    exit 2
    ;;
esac

if [ -f "$output" ] && [ "${FORCE_REBUILD:-0}" != 1 ]; then
  log "cache=hit path=$output bytes=$(wc -c < "$output")"
  exit 0
fi

if [ "$(uname -s)" != Linux ]; then
  echo "error: kernel builds require a Linux host (got $(uname -s))" >&2
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
script_dir=$(cd "$(dirname "$script_path")" && pwd)

# shellcheck source=scripts/microvm/lib-arch.sh
. "$script_dir/lib-arch.sh"
if [ "$HOST_ARCH" != "$arch" ]; then
  if [ "${MICROVM_CROSS_COMPILE:-0}" != 1 ]; then
    echo "error: requested a $arch kernel on a $(uname -m) host; set MICROVM_CROSS_COMPILE=1 for an explicit cross-build" >&2
    exit 1
  fi
  case "$arch" in
    arm64) CROSS_COMPILE=${CROSS_COMPILE:-aarch64-linux-gnu-} ;;
    amd64) CROSS_COMPILE=${CROSS_COMPILE:-x86_64-linux-gnu-} ;;
  esac
  export CROSS_COMPILE
fi

kernel_cc=${CROSS_COMPILE:-}gcc
for cmd in curl sha256sum tar make "$kernel_cc" bc flex bison nproc; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "error: '$cmd' is required on PATH" >&2; exit 1; }
done

work=$(mktemp -d "${TMPDIR:-/tmp}/secure-oci-base-kernel.XXXXXX")
trap 'rm -rf "$work"' EXIT

log "cache=miss version=$KERNEL_VERSION architecture=$arch"
log "phase=download url=$KERNEL_URL"
curl --fail --silent --show-error --location --retry 3 --retry-all-errors \
  --connect-timeout 10 --max-time 300 --output "$work/linux.tar.xz" "$KERNEL_URL"
log "phase=checksum archive_bytes=$(wc -c < "$work/linux.tar.xz")"
echo "${KERNEL_SHA256}  $work/linux.tar.xz" | sha256sum --check

log "phase=extract"
tar -xf "$work/linux.tar.xz" -C "$work"
src="$work/linux-${KERNEL_VERSION}"

(
  cd "$src"
  export ARCH="$kernel_arch"
  # Pin the version string kbuild embeds (LINUX_COMPILE_BY/HOST and the
  # human-readable build timestamp) so at least that part of the image
  # doesn't vary between builds. This does NOT make the kernel image fully
  # byte-reproducible on its own - see the note above build-kernel.sh's
  # invocation in run-microvm.sh's wiki page for the known residual gap.
  export KBUILD_BUILD_TIMESTAMP="Thu Jan  1 00:00:00 UTC 1970"
  export KBUILD_BUILD_USER=builder
  export KBUILD_BUILD_HOST=reproducible
  export SOURCE_DATE_EPOCH=0
  # merge_config.sh itself runs `make alldefconfig` to resolve the rest
  # non-interactively - not a separate oldconfig step.
  ./scripts/kconfig/merge_config.sh "$defconfig" \
    "$script_dir/kernel-common.config" \
    "$script_dir/kernel-$arch.config"
  log "phase=compile target=$make_target jobs=$(nproc)"
  make -j"$(nproc)" "$make_target"
)

mkdir -p "$(dirname "$output")"
cp "$src/$built_image" "$output"
cp "$src/.config" "$(dirname "$output")/kernel.config.resolved"

# A minimal, checksum-backed provenance/SBOM record for the guest kernel
# component - what version, where it came from, and what it resolved to.
# Written into the same cache directory as the kernel image itself, so a CI
# cache hit (which skips the rest of this script) still carries it forward.
kernel_sha256=$(sha256sum "$output" | cut -d' ' -f1)
config_sha256=$(sha256sum "$(dirname "$output")/kernel.config.resolved" | cut -d' ' -f1)
cat > "$(dirname "$output")/kernel.provenance.json" <<EOF
{
  "schema_version": 1,
  "component": "microvm-kernel",
  "kernel_version": "$KERNEL_VERSION",
  "architecture": "$arch",
  "source_url": "$KERNEL_URL",
  "source_sha256": "$KERNEL_SHA256",
  "kernel_image_sha256": "$kernel_sha256",
  "resolved_config_sha256": "$config_sha256"
}
EOF

# A minimal but standard-format SBOM for the guest kernel component (the
# "guest OS" half of the microVM), in CycloneDX 1.5: a single
# operating-system component, its hash, and where it came from. Separate
# from kernel.provenance.json above, which is this project's own compact
# record consumed by write-microvm-boot-manifest.py and the Cosign-signed
# evidence bundle.
bom_serial="$(python3 -c 'import sys, uuid; print("urn:uuid:" + str(uuid.uuid5(uuid.NAMESPACE_URL, sys.argv[1])))' \
  "$KERNEL_URL#sha256:$kernel_sha256")"
cat > "$(dirname "$output")/kernel.sbom.cdx.json" <<EOF
{
  "bomFormat": "CycloneDX",
  "specVersion": "1.5",
  "serialNumber": "$bom_serial",
  "version": 1,
  "components": [
    {
      "type": "operating-system",
      "bom-ref": "linux-kernel@$KERNEL_VERSION",
      "name": "linux",
      "version": "$KERNEL_VERSION",
      "purl": "pkg:generic/linux@$KERNEL_VERSION",
      "hashes": [
        {"alg": "SHA-256", "content": "$kernel_sha256"}
      ],
      "properties": [
        {"name": "secure-oci-base:resolved-config-sha256", "value": "$config_sha256"},
        {"name": "secure-oci-base:source-archive-sha256", "value": "$KERNEL_SHA256"}
      ],
      "externalReferences": [
        {"type": "distribution", "url": "$KERNEL_URL"}
      ]
    }
  ]
}
EOF

log "phase=complete path=$output bytes=$(wc -c < "$output") version=$KERNEL_VERSION sha256=$KERNEL_SHA256"
log "phase=provenance path=$(dirname "$output")/kernel.provenance.json kernel_sha256=$kernel_sha256 config_sha256=$config_sha256"
log "phase=sbom path=$(dirname "$output")/kernel.sbom.cdx.json"

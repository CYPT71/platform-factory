#!/usr/bin/env bash
# Deterministically assembles a gzip-compressed cpio (newc) initramfs from an
# already-extracted OCI layer tar plus a single init binary placed at
# /sbin/init. Byte-for-byte reproducible given identical inputs: cpio's own
# device/inode fields are zeroed (--reproducible), entries are packed in a
# fixed sorted order, and the freshly-copied init binary (and the /sbin
# directory created to hold it, neither of which come from the layer tar) has
# its mtime pinned to epoch 0 to match the layer's own fixed timestamps -
# otherwise the wall-clock time of the machine running this script would leak
# into the archive bytes.
set -euo pipefail

if [ "$#" -lt 3 ]; then
  echo "usage: $0 LAYER_TAR_GZ INIT_BINARY OUTPUT_INITRAMFS_GZ [ENTRYPOINT...]" >&2
  exit 2
fi
layer_tar_gz=$1
init_binary=$2
output=$3
shift 3

rootfs=$(mktemp -d "${TMPDIR:-/tmp}/platform-factory-base-initramfs.XXXXXX")
trap 'rm -rf "$rootfs"' EXIT

# --delay-directory-restore: without it, GNU tar restores a directory's
# mtime as soon as it creates the directory, then leaves it wherever the
# OS bumps it to once files are subsequently written inside (e.g. app/,
# which contains a file) - so it wouldn't stay pinned at the layer's fixed
# epoch-0 timestamps otherwise, and this whole archive wouldn't reproduce.
tar --delay-directory-restore -xzf "$layer_tar_gz" -C "$rootfs"
mkdir -p "$rootfs/sbin"
cp "$init_binary" "$rootfs/sbin/init"
chmod 0555 "$rootfs/sbin/init"
touch -h -d @0 "$rootfs/sbin/init" "$rootfs/sbin"
if [ "$#" -gt 0 ]; then
  mkdir -p "$rootfs/etc/platform-factory"
  python3 -c 'import json,sys; json.dump(sys.argv[2:], open(sys.argv[1], "w"))' \
    "$rootfs/etc/platform-factory/entrypoint.json" "$@"
  chmod 0444 "$rootfs/etc/platform-factory/entrypoint.json"
  touch -h -d @0 "$rootfs/etc/platform-factory" "$rootfs/etc/platform-factory/entrypoint.json"
fi

( cd "$rootfs" && find . -mindepth 1 -print0 | sort -z | cpio --null -o -H newc --reproducible 2>/dev/null ) \
  | gzip -9 -n > "$output"

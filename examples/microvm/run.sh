#!/usr/bin/env bash
set -euo pipefail
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH= cd -- "$here/../.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/secure-oci-example-microvm.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
cd "$repo"
go build -trimpath -o "$work/platform-factory" ./cmd/platform-factory
available=1
"$work/platform-factory" microvm probe || available=0
if [ "$#" -eq 0 ]; then
  echo "probe complete (available=$available); pass KERNEL INITRAMFS to perform a native boot"
  exit 0
fi
if [ "$available" -ne 1 ]; then
  echo "native virtualization is unavailable on this host" >&2
  exit 1
fi
if [ "$#" -ne 2 ]; then
  echo "usage: $0 [KERNEL INITRAMFS]" >&2
  exit 2
fi
for input in "$1" "$2"; do test -r "$input"; done
digest() { shasum -a 256 "$1" | awk '{print "sha256:" $1}'; }
"$work/platform-factory" microvm run --kernel "$1" --kernel-digest "$(digest "$1")" \
  --initramfs "$2" --initramfs-digest "$(digest "$2")" --memory-mib 256 --vcpus 1

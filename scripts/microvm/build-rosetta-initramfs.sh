#!/usr/bin/env bash
# Build the ARM64 PID 1 + static AMD64 probe used by the real Rosetta proof.
# This only cross-compiles and assembles files; it never installs Rosetta.
set -euo pipefail
if [ "$#" -ne 1 ]; then
  echo "usage: $0 OUTPUT_INITRAMFS" >&2
  exit 2
fi
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
output=$1
mkdir -p "$(dirname "$output")"
output=$(cd "$(dirname "$output")" && pwd -P)/$(basename "$output")
work=$(mktemp -d "${TMPDIR:-/tmp}/pf-rosetta-fixture.XXXXXX")
trap 'rm -rf "$work"' EXIT
cd "$repo_root"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' \
  -o "$work/init" ./tests/microvm/rosetta-init
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
  -o "$work/amd64-probe" ./tests/microvm/rosetta-probe
mkdir "$work/rootfs"
cp "$work/init" "$work/rootfs/init"
cp "$work/amd64-probe" "$work/rootfs/amd64-probe"
go run scripts/microvm/assemble-rosetta-initramfs.go "$work/rootfs" "$work/rosetta-initramfs.cpio.gz"
test -s "$work/rosetta-initramfs.cpio.gz"
mv "$work/rosetta-initramfs.cpio.gz" "$output"

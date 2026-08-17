#!/usr/bin/env bash
set -euo pipefail
script_path=${BASH_SOURCE[0]}
repo_root=$(cd "$(dirname "$script_path")/../.." && pwd)
output=${1:-dist/packages}
version=${2:-$(git -C "$repo_root" describe --tags --always --dirty 2>/dev/null || echo dev)}
mkdir -p "$output"
output=$(cd "$output" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/platform-factory-package.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
export GOCACHE="$work/go-build-cache"
mkdir -p "$GOCACHE"
for target in linux/amd64 darwin/arm64 windows/amd64; do
  os=${target%/*}; arch=${target#*/}; env="$work/$os-$arch"
  "$repo_root/scripts/local/bootstrap.sh" --target "$target" --env "$env"
  extension=tar.gz; [ "$os" = windows ] && extension=zip
  archive="$output/platform-factory-$version-$os-$arch.$extension"
  (cd "$repo_root" && go run ./cmd/platform-factory-packager --env "$env" --out "$archive")
done
(cd "$output" && shasum -a 256 platform-factory-"$version"-* > SHA256SUMS)
(cd "$output" && shasum -a 256 -c SHA256SUMS >/dev/null)
echo "release packages ready in $output" >&2

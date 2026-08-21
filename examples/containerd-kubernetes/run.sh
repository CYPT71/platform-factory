#!/usr/bin/env bash
set -euo pipefail
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH= cd -- "$here/../.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/platform-factory-containerd.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
cd "$repo"
(cd plugins/containerd && GOWORK=off go run ./cmd/platform-factory-containerd config) >"$work/config.toml"
(cd plugins/containerd && GOWORK=off go run ./cmd/platform-factory-containerd runtimeclass) >"$work/runtimeclass.yaml"
test -s "$work/config.toml"
test -s "$work/runtimeclass.yaml"
echo "generated: $work/config.toml"
echo "generated: $work/runtimeclass.yaml"
if [ "${APPLY:-0}" = 1 ]; then
  command -v kubectl >/dev/null
  kubectl apply -f "$work/runtimeclass.yaml"
fi

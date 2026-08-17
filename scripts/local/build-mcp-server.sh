#!/usr/bin/env bash
# Build the platform-factory MCP server image (see
# scripts/ci/build-mcp-image-layout.sh, the same native OCI layout this
# repository's CI pushes to GHCR) and load it straight into a local
# Docker or Podman image store - no Dockerfile, no `docker build`, no
# registry round trip. `platform-factory import` (cmd/platform-factory/import.go)
# already does exactly this for any native layout: it streams the layout
# to `docker load`/`podman load` in whichever archive format that
# runtime's loader expects.
set -euo pipefail

usage() {
  echo "usage: $0 [--runtime docker|podman]" >&2
  echo "  --runtime   defaults to docker if present on PATH, else podman" >&2
}

# Fixed, not a flag: scripts/ci/build-mcp-image-layout.sh always builds
# the layout with this exact embedded reference (-image
# platform-factory-mcp -tag latest), and `platform-factory import`
# requires the IMAGE it's given to match the layout's own recorded
# reference exactly - `docker/podman tag platform-factory-mcp:latest
# whatever:you-want` afterward if you need a different local name.
tag="platform-factory-mcp:latest"
runtime=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --runtime) runtime=$2; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage; exit 2 ;;
  esac
done

if [ -z "$runtime" ]; then
  if command -v docker >/dev/null 2>&1; then
    runtime=docker
  elif command -v podman >/dev/null 2>&1; then
    runtime=podman
  else
    echo "neither docker nor podman found on PATH; install one or pass --runtime" >&2
    exit 1
  fi
fi
if [ "$runtime" != docker ] && [ "$runtime" != podman ]; then
  echo "--runtime must be docker or podman, got: $runtime" >&2
  exit 2
fi
if ! command -v "$runtime" >/dev/null 2>&1; then
  echo "$runtime is not on PATH" >&2
  exit 1
fi

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$repo_root"

host_goarch=$(go env GOHOSTARCH)
case "$host_goarch" in
  amd64|arm64) ;;
  *) echo "unsupported host architecture for the MCP image: $host_goarch (need amd64 or arm64)" >&2; exit 1 ;;
esac

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/build-mcp-server.XXXXXX")
trap 'rm -rf "$work_dir"' EXIT

echo "building the MCP server OCI layout natively for linux/$host_goarch..." >&2
scripts/ci/build-mcp-image-layout.sh "$work_dir/layout" "$work_dir/platform-factory" "$host_goarch"

echo "building platform-factory to run the import..." >&2
CGO_ENABLED=0 go build -trimpath -o "$work_dir/pf" ./cmd/platform-factory

echo "loading the layout into $runtime as $tag..." >&2
"$work_dir/pf" import --runtime "$runtime" --layout "$work_dir/layout" "$tag"

cat >&2 <<EOF

Loaded $tag into $runtime. Try it:

  printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}\n' |
    $runtime run --rm -i -v "\$(pwd):/workspace" $tag mcp serve --repo /workspace
EOF

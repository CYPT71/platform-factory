#!/usr/bin/env bash
set -euo pipefail
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH= cd -- "$here/../.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/secure-oci-example-reproducible.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
cd "$repo"
go build -trimpath -o "$work/platform-factory" ./cmd/platform-factory
go_path=$(go env GOROOT)/bin/go
sed "s| go | $go_path |g" ./examples/pipeline.json >"$work/pipeline.json"
"$work/platform-factory" pipeline plan "$work/pipeline.json"
mkdir -p "$work/run-1/workspace" "$work/run-2/workspace"
cp -R ./examples/hello-pipeline/app/. "$work/run-1/workspace/"
cp -R ./examples/hello-pipeline/app/. "$work/run-2/workspace/"
"$work/platform-factory" pipeline run --workdir "$work/run-1" --cache "$work/cache" "$work/pipeline.json"
"$work/platform-factory" pipeline run --workdir "$work/run-2" --cache "$work/cache" "$work/pipeline.json"
echo "two independent runs completed against one explicit CAS: $work/cache"

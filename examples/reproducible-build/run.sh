#!/usr/bin/env bash
set -euo pipefail
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH= cd -- "$here/../.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/platform-factory-example-reproducible.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
cd "$repo"
go build -trimpath -o "$work/platform-factory" ./cmd/platform-factory
go_path=$(go env GOROOT)/bin/go
sed "s| go | $go_path |g" ./examples/pipeline.json >"$work/pipeline.json"
"$work/platform-factory" pipeline plan "$work/pipeline.json"
# --sandbox off: this example demonstrates reproducible caching, not
# namespace isolation, and its pipeline.json declares no base rootfs, so a
# sandboxed run would have no sh/go toolchain inside the pivoted root. This
# mirrors reproduce-ci-quality.sh's own pipeline_end_to_end, which runs its
# demo pipeline the same way.
"$work/platform-factory" pipeline run --sandbox off --workdir "$work/run-1" --cache "$work/cache" "$work/pipeline.json"
"$work/platform-factory" pipeline run --sandbox off --workdir "$work/run-2" --cache "$work/cache" "$work/pipeline.json"
echo "two independent runs completed against one explicit CAS: $work/cache"

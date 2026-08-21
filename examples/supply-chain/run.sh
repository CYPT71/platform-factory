#!/usr/bin/env bash
set -euo pipefail
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH= cd -- "$here/../.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/platform-factory-supply-chain.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
cd "$repo"
go build -trimpath -o "$work/platform-factory" ./cmd/platform-factory
if "$work/platform-factory" evidence --reproducible ./examples/pipeline.json >"$work/evidence.json"; then
  :
else
  code=$?
  test "$code" -eq 1
fi
"$work/platform-factory" sbom ./examples/hello-pipeline/app >"$work/sbom.cdx.json"
test -s "$work/evidence.json"
test -s "$work/sbom.cdx.json"
echo "native evidence and SBOM generated under $work"

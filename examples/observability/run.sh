#!/usr/bin/env bash
set -euo pipefail
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH= cd -- "$here/../.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/platform-factory-observability.XXXXXX")
report_and_clean() {
  status=$?
  if [ "$status" -ne 0 ] && [ -s "$work/events.jsonl" ]; then
    echo "--- $work/events.jsonl (build failed, status=$status) ---" >&2
    cat "$work/events.jsonl" >&2
  fi
  rm -rf -- "$work"
}
trap report_and_clean EXIT
cd "$repo"
go build -trimpath -o "$work/platform-factory" ./cmd/platform-factory
# The static profile below rejects a dynamically linked ELF input, so this
# must be a static build regardless of the host's cgo defaults - the same
# reason ci-runtime.yml builds this binary with CGO_ENABLED=0.
CGO_ENABLED=0 go build -trimpath -o "$work/service" ./cmd/example-service
PLATFORM_FACTORY_TRACE_ID=example-trace "$work/platform-factory" build \
  --config ./examples/platform-factory.json --output "$work/layout" "$work/service" \
  2>"$work/events.jsonl"
test -s "$work/events.jsonl"
if grep -v '"trace_id":"example-trace"' "$work/events.jsonl"; then
  echo "an event lost the requested trace identifier" >&2
  exit 1
fi
echo "structured events: $work/events.jsonl"

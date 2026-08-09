#!/usr/bin/env bash
set -euo pipefail
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH= cd -- "$here/../.." && pwd)
cd "$repo"
for command in kind podman kubectl; do
  command -v "$command" >/dev/null || { echo "missing prerequisite: $command" >&2; exit 1; }
done
PLATFORM_FACTORY_KIND_PROVIDER=podman exec tests/kind/test-kind-distributed-cancellation.sh

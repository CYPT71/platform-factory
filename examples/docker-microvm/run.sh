#!/usr/bin/env bash
set -euo pipefail
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH= cd -- "$here/../.." && pwd)
if [ "$#" -ne 4 ]; then
  echo "usage: $0 OCI_LAYOUT IMAGE KERNEL MICROVM_INIT" >&2
  exit 2
fi
cd "$repo"
exec tests/microvm/test-docker-kvm.sh "$@"

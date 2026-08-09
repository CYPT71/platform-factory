#!/usr/bin/env bash
set -euo pipefail
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
run() {
  echo "=== running $1/run.sh ==="
  "$here/$1/run.sh"
}
run sdk
run project-config
run reproducible-build
run supply-chain
run observability
run containerd-kubernetes
run microvm
echo "portable examples passed; hardware/cluster examples expose their own run.sh entrypoints"

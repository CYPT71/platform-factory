#!/usr/bin/env bash
# .github/workflows/ci-kind-multinode.yml creates a real 1+2 node kind
# (Kubernetes-in-podman) cluster and runs distributed-runtime tests
# against it - a ~15 minute job on a dedicated CI runner. Inside an
# already-containerized dev environment, nesting a container-based cluster
# is frequently unreliable (cgroup/network nesting limits) even when the
# `kind` CLI and podman are present, so by default this script only checks
# the prerequisites are in place. Set PF_VERIFY_KIND_FULL=1 to attempt the
# full workflow (creates and tears down a real cluster).
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
export GOTOOLCHAIN=local KIND_EXPERIMENTAL_PROVIDER=podman PLATFORM_FACTORY_CONTAINER_ENGINE=podman

echo "--- Prerequisite check ---"
missing=0
if ! command -v podman >/dev/null 2>&1; then
  echo "MISSING: podman is not on PATH"
  missing=1
elif ! podman info >/dev/null 2>&1; then
  echo "MISSING: podman daemon is not reachable"
  missing=1
else
  echo "OK: podman is available and reachable"
fi
if command -v kind >/dev/null 2>&1 || [ -x "$(go env GOPATH 2>/dev/null)/bin/kind" ]; then
  echo "OK: kind CLI is available"
else
  echo "NOT INSTALLED: kind CLI (would be fetched with 'go install sigs.k8s.io/kind@v0.32.0' in the full run)"
fi

if [ "${PF_VERIFY_KIND_FULL:-0}" != "1" ]; then
  echo "kind-multinode-runtime: SKIP (prerequisite-only; set PF_VERIFY_KIND_FULL=1 to create a real cluster and run the full suite)"
  exit 0
fi

if [ "$missing" -ne 0 ]; then
  echo "kind-multinode-runtime: FAIL (PF_VERIFY_KIND_FULL=1 requested but prerequisites are missing)" >&2
  exit 1
fi

echo "--- Create the common one-plus-two kind cluster ---"
podman info
go install sigs.k8s.io/kind@v0.32.0
kind_bin="$(go env GOPATH)/bin/kind"
cleanup() { "$kind_bin" delete cluster --name platform-factory-distributed || true; }
trap cleanup EXIT
"$kind_bin" create cluster --name platform-factory-distributed \
  --wait 90s --config scripts/ci/kind-multinode.yaml \
  --image kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5

echo "--- Prove RuntimeClass scheduling contract ---"
PLATFORM_FACTORY_KIND_NAMESPACE=platform-factory-runtimeclass \
  go run ./plugins/containerd/cmd/platform-factory-containerd runtimeclass > platform-factory-runtimeclass.yaml
tests/kind/test-kind-runtimeclass.sh

echo "--- Prove distributed cancellation and restart durability ---"
tests/kind/test-kind-distributed-cancellation.sh

echo "--- Prove recovery from a real network partition ---"
tests/kind/test-kind-network-partition.sh

echo "--- Prove control-plane recovery after worker loss ---"
tests/kind/test-kind-worker-loss.sh

echo "kind-multinode-runtime: PASS"

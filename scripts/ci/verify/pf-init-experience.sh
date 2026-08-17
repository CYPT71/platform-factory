#!/usr/bin/env bash
# Local reproduction of .github/workflows/ci-pf-init-experience.yml: both
# the personas-and-tui job and the local-container-engines matrix (docker
# and podman), run sequentially instead of as separate matrix jobs.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
export GOTOOLCHAIN=local

echo "--- Validate the complete empty-repository and TUI experience ---"
go test ./cmd/platform-factory -count=1 -run 'TestPFInit|TestMarketplaceExperienceFromEmptyDirectory|TestResolveEcosystem|TestConfirmPlan|TestPluginCreate|TestPluginInstall'
go test ./internal/marketplace ./cmd/tui/marketplacetui -count=1

echo "--- Validate intermediate and senior clean-workspace experiences ---"
./demo/validate-personas.sh

for engine in docker podman; do
  if ! command -v "$engine" >/dev/null 2>&1; then
    echo "local-container-engines[$engine]: SKIP ($engine is not on PATH)"
    continue
  fi
  if ! "$engine" info >/dev/null 2>&1; then
    echo "local-container-engines[$engine]: SKIP ($engine daemon is not reachable)"
    continue
  fi
  echo "--- Validate the junior launch against a real local $engine ---"
  case "$engine" in
    docker) test_name=TestJuniorDeploysHelloWorldToRealLocalDocker ;;
    podman) test_name=TestJuniorDeploysHelloWorldToRealLocalPodman ;;
  esac
  PF_REQUIRE_REAL_RUNTIME=1 go test ./cmd/platform-factory -run "^${test_name}\$" -count=1 -v
  ./demo/validate.sh "$engine"
done

echo "pf-init-experience: PASS"

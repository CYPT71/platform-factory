#!/usr/bin/env bash
# Build, entitlement-sign, and execute the native HVF TCP+UDP guest test.
set -euo pipefail

if [ "$(uname -s)" != Darwin ] || [ "$(uname -m)" != arm64 ]; then
  echo "error: the HVF network test requires Apple-silicon macOS" >&2
  exit 1
fi
if [ -n "${BASH_VERSION:-}" ]; then
  script_path=${BASH_SOURCE[0]}
else
  script_path=${(%):-%x}
fi
repo_root=$(cd "$(dirname "$script_path")/../.." && pwd)
test -s "$repo_root/.cache/microvm/arm64/kernel"

test_binary=$(mktemp "${TMPDIR:-/tmp}/platform-factory-hvf-network.XXXXXX")
trap 'rm -f "$test_binary"' EXIT
(
  cd "$repo_root"
  CGO_ENABLED=1 go test -c ./cmd/platform-factory -o "$test_binary"
  codesign --force --sign - \
    --entitlements scripts/microvm/hvf.entitlements "$test_binary"
  codesign --verify --strict --verbose=2 "$test_binary"
  cd cmd/platform-factory
  PLATFORM_FACTORY_TEST_HVF_NETWORK=1 "$test_binary" \
    -test.run '^TestNativeHVFRealTCPAndUDP$' -test.count=1 -test.v
)

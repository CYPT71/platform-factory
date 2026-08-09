#!/usr/bin/env bash
# Compile, entitlement-sign and run the real Linux/HVF integration test on an
# Apple-silicon Mac. The caller supplies an arm64 Linux Image and, optionally,
# the OCI initramfs through the same opt-in environment contract as KVM CI.
set -euo pipefail

if [ "$(uname -s)" != Darwin ] || [ "$(uname -m)" != arm64 ]; then
  echo "error: the local HVF Linux test requires Apple-silicon macOS" >&2
  exit 1
fi

if [ -n "${BASH_VERSION:-}" ]; then
  script_path=${BASH_SOURCE[0]}
else
  script_path=${(%):-%x}
fi
repo_root=$(cd "$(dirname "$script_path")/../.." && pwd)
PLATFORM_FACTORY_TEST_KERNEL_IMAGE=${PLATFORM_FACTORY_TEST_KERNEL_IMAGE:-"$repo_root/.cache/microvm/arm64/kernel"}
PLATFORM_FACTORY_TEST_INITRD=${PLATFORM_FACTORY_TEST_INITRD:-"$repo_root/.cache/microvm/arm64/initramfs.cpio.gz"}
export PLATFORM_FACTORY_TEST_KERNEL_IMAGE PLATFORM_FACTORY_TEST_INITRD
test -s "$PLATFORM_FACTORY_TEST_KERNEL_IMAGE"
test -s "$PLATFORM_FACTORY_TEST_INITRD"

test_binary=$(mktemp "${TMPDIR:-/tmp}/secure-oci-vmm-hvf.XXXXXX")
trap 'rm -f "$test_binary"' EXIT

(
  cd "$repo_root"
  CGO_ENABLED=1 go test -c ./internal/hypervisor/hvf -o "$test_binary"
  codesign --force --sign - \
    --entitlements scripts/microvm/hvf.entitlements "$test_binary"
  codesign --verify --strict --verbose=2 "$test_binary"
  test_output=$(mktemp "${TMPDIR:-/tmp}/secure-oci-vmm-hvf-output.XXXXXX")
  trap 'rm -f "$test_output"' EXIT
  set +e
  "$test_binary" -test.run '^(TestRunLinuxWithRealHVF|TestDarwinVMMWithRealHVF)$' -test.count=1 -test.v \
    2>&1 | tee "$test_output"
  test_status=${PIPESTATUS[0]}
  set -e
  if [ "$test_status" -eq 0 ]; then
    exit 0
  fi
  if [ "${PLATFORM_FACTORY_ALLOW_UNAVAILABLE_HVF:-0}" = 1 ] &&
    grep -Fq 'Virtualization is not available on this hardware' "$test_output"; then
    echo "HVF execution is unavailable on this nested macOS runner; native Darwin contract tests remain mandatory"
    # Skip exactly the two hardware-dependent tests (already proven to fail
    # above for that reason) and run everything else, rather than an
    # allowlist of contract test names: a substring -test.run pattern here
    # previously re-matched TestDarwinVMMWithRealHVF itself (its name starts
    # with "TestDarwin"), so the "mandatory" fallback re-ran the same failing
    # hardware test and aborted under set -e instead of exiting 0.
    "$test_binary" -test.skip '^(TestRunLinuxWithRealHVF|TestDarwinVMMWithRealHVF)$' \
      -test.count=1 -test.v
    exit 0
  fi
  exit "$test_status"
)

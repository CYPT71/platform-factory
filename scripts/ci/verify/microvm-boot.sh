#!/usr/bin/env bash
# .github/workflows/ci-microvm.yml has three jobs: prepare-hvf-guest and
# boot-under-hvf require Apple's Hypervisor.framework on real macOS
# hardware and cannot run here at all (this is Linux). boot-under-kvm is a
# real ~50-minute job: cross-building a hardened Linux kernel from source,
# then booting it under KVM through the podman/docker/containerd
# lifecycles and a kernel-hardening scan. By default this script only
# proves the KVM prerequisites are real and exercises the project's own
# KVM VM/vCPU primitive directly (seconds, not tens of minutes). Set
# PF_VERIFY_MICROVM_FULL=1 to attempt the entire boot-under-kvm job.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
export GOTOOLCHAIN=local

echo "microvm-boot[prepare-hvf-guest, boot-under-hvf]: SKIP (require Apple Hypervisor.framework on macOS hardware; not reproducible here)"

echo "--- Confirm the runner exposes /dev/kvm ---"
test -e /dev/kvm
ls -la /dev/kvm
grep -Em1 'vmx|svm' /proc/cpuinfo || { echo "MISSING: no vmx/svm CPU flag"; exit 1; }

echo "--- Verify local KVM readiness (non-destructive check) ---"
scripts/microvm/check-kvm.sh

echo "--- Exercise the project-owned KVM VM and vCPU primitive ---"
go test ./internal/hypervisor/kvm -run '^TestRunFlatPayloadBootsAndHalts$' -count=1

if [ "${PF_VERIFY_MICROVM_FULL:-0}" != "1" ]; then
  echo "microvm-boot[boot-under-kvm]: SKIP (prerequisite + smoke test only; set PF_VERIFY_MICROVM_FULL=1 to run the full ~50 minute kernel build and boot suite)"
  exit 0
fi

echo "--- Full boot-under-kvm reproduction (this takes a long time) ---"
work=$(mktemp -d "${TMPDIR:-/tmp}/pf-verify-microvm.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags='-s -w -X main.debugExitEnabled=true' \
  -o "$work/example-service" ./cmd/example-service
go run ./cmd/oci-builder -binary "$work/example-service" -output "$work/oci-image" -arch amd64
mkdir -p .cache/microvm/amd64
timeout --signal=TERM 25m scripts/microvm/build-kernel.sh amd64 .cache/microvm/amd64/kernel
test -s .cache/microvm/amd64/kernel

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
  -o "$work/native-microvm-init" ./cmd/microvm-init
go build -trimpath -ldflags='-s -w' -o "$work/microvm-initramfs" ./cmd/microvm-initramfs
"$work/microvm-initramfs" -layout "$work/oci-image" -init "$work/native-microvm-init" \
  -output "$work/native-initramfs.cpio.gz"
PLATFORM_FACTORY_TEST_BZIMAGE="$(pwd)/.cache/microvm/amd64/kernel" \
  PLATFORM_FACTORY_TEST_INITRD="$work/native-initramfs.cpio.gz" \
  go test ./internal/hypervisor/kvm -run '^TestRunLinuxWithRealKVM$' -count=1 -v

MICROVM_KERNEL="$(pwd)/.cache/microvm/amd64/kernel" \
  MICROVM_SMOKE_TEST_PATH=/healthz \
  MICROVM_BOOT_MANIFEST="$work/boot-manifest.json" \
  timeout --signal=TERM 5m scripts/microvm/run-microvm.sh "$work/oci-image"
test -s "$work/boot-manifest.json"

echo "microvm-boot[boot-under-kvm]: PASS (core kernel-build-and-boot path; podman/docker/containerd lifecycle proofs and cosign signing were not reproduced)"

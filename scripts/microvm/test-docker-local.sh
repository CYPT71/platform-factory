#!/usr/bin/env bash
# Dev convenience wrapper around tests/microvm/test-docker-kvm.sh for
# reproducing Docker/native-KVM MicroVM failures on a local machine.
# NOT used by CI (.github/workflows/ci-microvm.yml drives
# test-docker-kvm.sh directly) - this exists only to absorb setup steps
# and environment quirks that a real CI runner already provides for free
# (a persistent kernel cache, a systemd-managed dockerd, absolute fixture
# paths) but a local dev sandbox usually doesn't. Keeping those quirks
# here instead of baking them into test-docker-kvm.sh itself keeps that
# script identical to what CI actually runs.
set -euo pipefail

if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
  echo "error: Docker/KVM proof requires Linux amd64" >&2
  exit 1
fi

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$repo_root"

for command in go docker sha256sum skopeo sudo; do
  command -v "$command" >/dev/null || {
    echo "error: $command is required; see scripts/microvm/check-kvm.sh for setup" >&2
    exit 1
  }
done

echo "==> checking /dev/kvm"
scripts/microvm/check-kvm.sh
# Some container-based dev sandboxes reset device permissions on every
# restart; a real CI runner's own setup step (ci-microvm.yml) does the
# equivalent chown/chmod once per job. This is best-effort - if the
# device is already usable, or this isn't that kind of sandbox, it's a
# harmless no-op.
if [ ! -r /dev/kvm ] || [ ! -w /dev/kvm ]; then
  echo "==> /dev/kvm is not read/write for $(id -un); requesting access"
  sudo chmod 666 /dev/kvm
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/platform-factory-docker-local.XXXXXX")
evidence_dir=${PLATFORM_FACTORY_EVIDENCE_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/platform-factory-docker-local-evidence.XXXXXX")}
mkdir -p "$evidence_dir"
cleanup() { rm -rf -- "$work"; }
trap cleanup EXIT

echo "==> building cmd/example-service and its OCI layout"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags='-s -w -X main.debugExitEnabled=true' \
  -o "$work/example-service" ./cmd/example-service
go run ./cmd/oci-builder -binary "$work/example-service" -output "$work/oci-image" -arch amd64 >&2

echo "==> building cmd/microvm-init"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
  -o "$work/native-microvm-init" ./cmd/microvm-init

kernel=$repo_root/.cache/microvm/amd64/kernel
if [ ! -s "$kernel" ]; then
  echo "==> no cached kernel at $kernel; building one (this takes several minutes)"
  scripts/microvm/build-kernel.sh amd64 "$kernel"
else
  echo "==> reusing cached kernel at $kernel"
fi

# This sandbox's dockerd has no systemd to receive `systemctl reload`
# (test-docker-kvm.sh's own reload step calls plain `systemctl reload
# docker`; many devcontainer images shim /usr/local/bin/systemctl to
# always exit 0 and print an explanatory message instead of failing, so
# that step's own exit code can't be used to detect this - check for
# /run/systemd/system directly instead, the same thing the shim itself
# checks). A real CI runner has systemd and that reload genuinely
# applies the new /etc/docker/daemon.json runtime registration. Send the
# same SIGHUP directly here as a local-only fallback so the registration
# takes effect before the test script's own docker run needs it - this
# is exactly what dockerd's systemd unit's ExecReload does under the
# hood.
if [ ! -d /run/systemd/system ]; then
  echo "==> no systemd in this environment; reloading dockerd via SIGHUP instead"
  sudo kill -HUP "$(pgrep -x dockerd | head -1)" 2>/dev/null || true
  sleep 1
fi

echo "==> running tests/microvm/test-docker-kvm.sh"
PLATFORM_FACTORY_EVIDENCE_DIR="$evidence_dir" \
  tests/microvm/test-docker-kvm.sh \
  "$work/oci-image" platform-factory:latest "$kernel" "$work/native-microvm-init"

echo "==> evidence written to $evidence_dir (kept after this script exits)"

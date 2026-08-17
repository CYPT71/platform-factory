#!/usr/bin/env bash
# Build cmd/example-service into an OCI layout and assemble it into a
# local container image via scripts/local/Dockerfile. macOS/Podman only;
# requires `go` and `podman`; touches nothing in the working tree.
set -euo pipefail

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: $0 <image-name[:tag]> [amd64|arm64]" >&2
  exit 2
fi
image=$1
arch=${2:-}

if [ -z "$arch" ]; then
  case "$(uname -m)" in
    arm64|aarch64) arch=arm64 ;;
    *) arch=amd64 ;;
  esac
fi
case "$arch" in
  amd64|arm64) ;;
  *) echo "error: unsupported architecture '$arch' (supported: amd64, arm64)" >&2; exit 2 ;;
esac

for cmd in go podman; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "error: '$cmd' is required on PATH" >&2; exit 1; }
done

if [ -n "${BASH_VERSION:-}" ]; then
  script_path=${BASH_SOURCE[0]}
elif [ -n "${ZSH_VERSION:-}" ]; then
  script_path=${(%):-%x}
else
  echo "error: this script requires Bash or Zsh" >&2
  exit 1
fi

repo_root=$(cd "$(dirname "$script_path")/../.." && pwd)
context=$(mktemp -d "${TMPDIR:-/tmp}/platform-factory-base-local.XXXXXX")
trap 'rm -rf "$context"' EXIT

echo "building static linux/$arch service binary..." >&2
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags='-s -w' -o "$context/service" "$repo_root/cmd/example-service"

echo "generating OCI image layout..." >&2
(
  cd "$repo_root"
  go run ./cmd/oci-builder \
    -binary "$context/service" \
    -output "$context/oci-image" \
    -arch "$arch" \
    -label security.tls.minimum=1.2
)

cp "$repo_root/scripts/local/Dockerfile" "$context/Dockerfile"

echo "building local image '$image'..." >&2
podman build --platform "linux/$arch" --tag "$image" "$context"

cat <<MSG

built $image (linux/$arch)

run it locally:
  podman run --rm --read-only --security-opt no-new-privileges \\
    --tmpfs /tmp:rw,noexec,nosuid,size=16m --publish 8080:8080 $image

check it:
  curl http://localhost:8080/healthz
  curl http://localhost:8080/ping
  curl http://localhost:8080/metrics

logs are structured JSON on the container's stdout (podman logs <container>).
MSG

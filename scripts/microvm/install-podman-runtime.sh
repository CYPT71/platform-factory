#!/usr/bin/env bash
# Build and register platform-factory-runtime for the current user's Podman config.
# This is Linux-only because the selected runtime owns /dev/kvm directly.
set -euo pipefail

if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
  echo "error: the native Podman/KVM runtime currently requires Linux amd64" >&2
  exit 1
fi

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd "$script_dir/../.." && pwd)
install_dir=${PLATFORM_FACTORY_RUNTIME_INSTALL_DIR:-"$HOME/.local/bin"}
config_root=${XDG_CONFIG_HOME:-"$HOME/.config"}
dropin_dir="$config_root/containers/containers.conf.d"
runtime_path="$install_dir/platform-factory-runtime"
dropin_path="$dropin_dir/90-platform-factory-runtime.conf"

mkdir -p "$install_dir" "$dropin_dir"
temporary=$(mktemp "$install_dir/.platform-factory-runtime.XXXXXX")
trap 'rm -f "$temporary"' EXIT
(
  cd "$repo_root"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags='-s -w' -o "$temporary" ./cmd/platform-factory-runtime
)
chmod 0755 "$temporary"
mv "$temporary" "$runtime_path"

config_temporary=$(mktemp "$dropin_dir/.platform-factory-runtime.XXXXXX")
trap 'rm -f "$config_temporary"' EXIT
{
  echo '[engine.runtimes]'
  printf 'platform-factory = ["%s"]\n' "$runtime_path"
  printf 'platform-factory-runtime = ["%s"]\n' "$runtime_path"
} >"$config_temporary"
chmod 0600 "$config_temporary"
mv "$config_temporary" "$dropin_path"

echo "installed runtime: $runtime_path"
echo "installed Podman config: $dropin_path"
echo "verify with: podman info --format '{{json .Host.OCIRuntime}}'"

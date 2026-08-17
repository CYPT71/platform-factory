#!/usr/bin/env bash
set -euo pipefail

if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
  echo "SKIP: containerd runtime installer requires Linux amd64"
  exit 0
fi

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
work_root=$(mktemp -d "${TMPDIR:-/tmp}/pf-containerd-install.XXXXXX")
cleanup() { chmod -R u+w "$work_root" 2>/dev/null || true; rm -rf -- "$work_root"; }
trap cleanup EXIT

install_dir="$work_root/bin"
config_dir="$work_root/config"
state_dir="$work_root/state"
mkdir -p "$install_dir" "$config_dir"
printf 'operator-owned\n' >"$config_dir/90-platform-factory-runtime.toml"

run_installer() {
  PLATFORM_FACTORY_RUNTIME_INSTALL_DIR="$install_dir" \
  PLATFORM_FACTORY_CONTAINERD_CONFIG_DIR="$config_dir" \
  PLATFORM_FACTORY_CONTAINERD_STATE_DIR="$state_dir" \
  PLATFORM_FACTORY_KVM_DEVICE=/dev/null \
    "$repo_root/scripts/microvm/install-containerd-runtime.sh" "$@"
}

run_installer install >/dev/null
first=$(find "$install_dir" "$config_dir" -type f -exec sha256sum {} \; | sort)
run_installer install >/dev/null
second=$(find "$install_dir" "$config_dir" -type f -exec sha256sum {} \; | sort)
test "$first" = "$second"
run_installer probe | grep -q 'integration ready'
run_installer uninstall >/dev/null
test "$(cat "$config_dir/90-platform-factory-runtime.toml")" = operator-owned
test ! -e "$install_dir/platform-factory-runtime"
test ! -e "$install_dir/containerd-shim-platform-factory-v1"
test ! -e "$install_dir/platform-factory-containerd"
printf '✅ containerd runtime install is idempotent and rollback restores originals\n'

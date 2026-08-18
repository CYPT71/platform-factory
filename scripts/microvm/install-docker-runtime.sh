#!/usr/bin/env bash
# Build and register platform-factory-runtime as a named Docker runtime.
# This is Linux-only because the selected runtime owns /dev/kvm directly.
#
# Unlike Podman's containers.conf.d (a directory of independent drop-in
# files - see install-podman-runtime.sh), Docker has a single
# /etc/docker/daemon.json a caller must merge into rather than overwrite,
# since it may already carry unrelated daemon settings. jq does that merge;
# this script never round-trips the file through anything else, and never
# touches any key but .runtimes["platform-factory"|"platform-factory-runtime"].
#
# dockerd does not pick up daemon.json changes until reloaded - this script
# writes the file and stops there. Restarting a running daemon is a
# disruptive action on shared infrastructure and is left to the operator,
# printed as the next explicit step rather than run automatically.
set -euo pipefail

if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
  echo "error: the native Docker/KVM runtime currently requires Linux amd64" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "error: jq is required to safely merge /etc/docker/daemon.json" >&2
  exit 1
fi

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd "$script_dir/../.." && pwd)
install_dir=${PLATFORM_FACTORY_RUNTIME_INSTALL_DIR:-"$HOME/.local/bin"}
daemon_json=${PLATFORM_FACTORY_DOCKER_DAEMON_JSON:-"/etc/docker/daemon.json"}
runtime_path="$install_dir/platform-factory-runtime"

# install_dir is user-owned (defaults to $HOME/.local/bin) and never needs
# root; daemon_json defaults to /etc/docker, which does. Everything below
# that touches daemon_json's own directory goes through sudo explicitly,
# rather than sudo'ing the whole script, so the build and the runtime
# binary install above stay attributed to the invoking user (a sudo'd
# `go build` would instead try to write to root's module/build caches,
# which likely do not exist and are not what this script is meant to
# populate).
mkdir -p "$install_dir"
temporary=$(mktemp "$install_dir/.platform-factory-runtime.XXXXXX")
trap 'rm -f "$temporary"' EXIT
(
  cd "$repo_root"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags='-s -w' -o "$temporary" ./cmd/platform-factory-runtime
)
chmod 0755 "$temporary"
mv "$temporary" "$runtime_path"

sudo mkdir -p "$(dirname "$daemon_json")"
backed_up=0
if [ -e "$daemon_json" ]; then
  if ! jq empty "$daemon_json" >/dev/null 2>&1; then
    echo "error: $daemon_json is not valid JSON; refusing to merge into it" >&2
    exit 1
  fi
  sudo cp "$daemon_json" "$daemon_json.platform-factory-backup.$(date +%s)"
  backed_up=1
else
  echo '{}' | sudo tee "$daemon_json" >/dev/null
fi

# A same-directory temp file (as install-podman-runtime.sh's drop-in
# config uses) would itself need root just to create, since daemon_json's
# directory is root-owned; write the merged result somewhere the invoking
# user can, and let `sudo install` place it atomically with the right
# owner and mode in one step instead.
config_temporary=$(mktemp "${TMPDIR:-/tmp}/.platform-factory-runtime-daemon.XXXXXX")
trap 'rm -f "$config_temporary"' EXIT
jq --arg path "$runtime_path" \
  '.runtimes["platform-factory"] = {"path": $path} | .runtimes["platform-factory-runtime"] = {"path": $path}' \
  "$daemon_json" >"$config_temporary"
sudo install -o root -g root -m 0644 "$config_temporary" "$daemon_json"

echo "installed runtime: $runtime_path"
if [ "$backed_up" -eq 1 ]; then
  echo "updated Docker config: $daemon_json (previous version backed up alongside it)"
else
  echo "wrote Docker config: $daemon_json (did not previously exist)"
fi
echo "next: reload the Docker daemon for this to take effect, e.g. sudo systemctl reload docker"
echo "verify with: docker run --rm --runtime=platform-factory hello-world"

#!/usr/bin/env bash
# Install the native OCI runtime, its containerd shim, and a containerd
# config-v2 import fragment. Run as root, or override PLATFORM_FACTORY_* paths for
# a staged/local installation.
set -euo pipefail

if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
  echo "error: the native containerd/KVM runtime currently requires Linux amd64" >&2
  exit 1
fi

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd "$script_dir/../.." && pwd)
install_dir=${PLATFORM_FACTORY_RUNTIME_INSTALL_DIR:-/usr/local/bin}
config_dir=${PLATFORM_FACTORY_CONTAINERD_CONFIG_DIR:-/etc/containerd/conf.d}
runtime_path="$install_dir/platform-factory-runtime"
shim_path="$install_dir/containerd-shim-platform-factory-v1"
helper_path="$install_dir/platform-factory-containerd"
fragment_path="$config_dir/90-platform-factory-runtime.toml"
runtimeclass_path=${PLATFORM_FACTORY_RUNTIMECLASS_PATH:-"$config_dir/platform-factory-runtimeclass.yaml"}

mkdir -p "$install_dir" "$config_dir"

# platform-factory-runtime lives in the main module (the OCI runtime spec
# facade both Podman and platform-factory-shim drive); platform-factory-shim
# and platform-factory-containerd are the plugins/containerd module's own
# binaries - see plugins/containerd/go.mod. containerd-shim-platform-factory-v1
# is containerd's own required name for the runtime_type
# "io.containerd.platform-factory.v1" platform-factory-containerd's generated
# config selects (see plugins/containerd/internal/containerdshim).
for pair in \
  "cmd/platform-factory-runtime:platform-factory-runtime" \
  "plugins/containerd/cmd/platform-factory-shim:containerd-shim-platform-factory-v1" \
  "plugins/containerd/cmd/platform-factory-containerd:platform-factory-containerd"; do
  package=${pair%%:*}
  target=${pair##*:}
  temporary=$(mktemp "$install_dir/.${target}.XXXXXX")
  (
    cd "$repo_root"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
      -ldflags='-s -w' -o "$temporary" "./$package"
  )
  chmod 0755 "$temporary"
  mv "$temporary" "$install_dir/$target"
done

config_temporary=$(mktemp "$config_dir/.secure-oci-containerd.XXXXXX")
"$helper_path" config >"$config_temporary"
chmod 0644 "$config_temporary"
mv "$config_temporary" "$fragment_path"

class_temporary=$(mktemp "$config_dir/.secure-oci-runtimeclass.XXXXXX")
"$helper_path" runtimeclass >"$class_temporary"
chmod 0644 "$class_temporary"
mv "$class_temporary" "$runtimeclass_path"

echo "installed runtime: $runtime_path"
echo "installed shim: $shim_path"
echo "installed containerd fragment: $fragment_path"
echo "ensure config.toml version=2 imports \"$config_dir/*.toml\", then restart containerd"
echo "install RuntimeClass with: kubectl apply -f $runtimeclass_path"

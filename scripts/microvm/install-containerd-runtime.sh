#!/usr/bin/env bash
# Idempotently install, inspect, or uninstall the native OCI runtime, its
# containerd shim, and its configuration. Run as root, or override the
# PLATFORM_FACTORY_* paths for a staged/local installation.
set -euo pipefail

usage() {
  cat <<'EOF'
usage: install-containerd-runtime.sh [install|probe|uninstall] [--node NAME]

  install          build and atomically install files (default)
  probe            verify host KVM and installed files without changing state
  uninstall        remove owned files and restore pre-installation backups
  --node NAME      label and taint this Kubernetes node after a successful probe;
                   uninstall removes only those platform-factory markers
EOF
}

action=install
node_name=
if [ "$#" -gt 0 ] && { [ "$1" = install ] || [ "$1" = probe ] || [ "$1" = uninstall ]; }; then
  action=$1
  shift
fi
while [ "$#" -gt 0 ]; do
  case "$1" in
    --node) [ "$#" -ge 2 ] || { echo "error: --node requires a name" >&2; exit 2; }; node_name=$2; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown option '$1'" >&2; usage >&2; exit 2 ;;
  esac
done
case "$node_name" in
  "") ;;
  .*|-*|*[-.]|*[!a-z0-9.-]*) echo "error: invalid Kubernetes node name '$node_name'" >&2; exit 2 ;;
esac

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd "$script_dir/../.." && pwd)
install_dir=${PLATFORM_FACTORY_RUNTIME_INSTALL_DIR:-/usr/local/bin}
config_dir=${PLATFORM_FACTORY_CONTAINERD_CONFIG_DIR:-/etc/containerd/conf.d}
runtime_path="$install_dir/platform-factory-runtime"
shim_path="$install_dir/containerd-shim-platform-factory-v1"
helper_path="$install_dir/platform-factory-containerd"
fragment_path="$config_dir/90-platform-factory-runtime.toml"
runtimeclass_path=${PLATFORM_FACTORY_RUNTIMECLASS_PATH:-"$config_dir/platform-factory-runtimeclass.yaml"}
state_dir=${PLATFORM_FACTORY_CONTAINERD_STATE_DIR:-"$config_dir/platform-factory-install.state"}
label_key=platform-factory.dev/runtime-platform-factory

probe_host() {
  kvm_device=${PLATFORM_FACTORY_KVM_DEVICE:-/dev/kvm}
  [ -c "$kvm_device" ] || { echo "error: $kvm_device is not a character device" >&2; return 1; }
  [ -r "$kvm_device" ] && [ -w "$kvm_device" ] || { echo "error: $kvm_device is not readable and writable by this operator" >&2; return 1; }
}

managed_paths="$runtime_path $shim_path $helper_path $fragment_path $runtimeclass_path"

if [ "$action" = uninstall ]; then
  if [ -n "$node_name" ]; then
    command -v kubectl >/dev/null 2>&1 || { echo "error: kubectl is required with --node" >&2; exit 1; }
    kubectl label node "$node_name" "$label_key-"
    kubectl taint node "$node_name" "$label_key:NoSchedule-" >/dev/null 2>&1 || true
  fi
  for path in $managed_paths; do
    marker="$state_dir/$(basename "$path").owned"
    backup="$state_dir/$(basename "$path").backup"
    if [ -f "$marker" ]; then
      rm -f -- "$path" "$marker"
      if [ -f "$backup" ]; then
        mv "$backup" "$path"
      fi
    fi
  done
  rmdir "$state_dir" 2>/dev/null || true
  echo "platform-factory containerd integration uninstalled"
  exit 0
fi

if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
  echo "error: the native containerd/KVM runtime currently requires Linux amd64" >&2
  exit 1
fi

if [ "$action" = probe ]; then
  probe_host
  for path in $managed_paths; do
    [ -f "$path" ] || { echo "error: installed file is missing: $path" >&2; exit 1; }
  done
  echo "platform-factory containerd integration ready"
  exit 0
fi

mkdir -p "$install_dir" "$config_dir" "$state_dir"

remember_original() {
  path=$1
  marker="$state_dir/$(basename "$path").owned"
  backup="$state_dir/$(basename "$path").backup"
  if [ ! -f "$marker" ]; then
    if [ -e "$path" ] || [ -L "$path" ]; then
      cp -p "$path" "$backup"
    fi
    : >"$marker"
  fi
}

install_file() {
  source=$1 target=$2 mode=$3
  remember_original "$target"
  if [ -f "$target" ] && cmp -s "$source" "$target"; then
    rm -f "$source"
    return
  fi
  chmod "$mode" "$source"
  mv "$source" "$target"
}

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
  install_file "$temporary" "$install_dir/$target" 0755
done

config_temporary=$(mktemp "$config_dir/.platform-factory-containerd.XXXXXX")
"$helper_path" config >"$config_temporary"
install_file "$config_temporary" "$fragment_path" 0644

class_temporary=$(mktemp "$config_dir/.platform-factory-runtimeclass.XXXXXX")
"$helper_path" runtimeclass >"$class_temporary"
install_file "$class_temporary" "$runtimeclass_path" 0644

if [ -n "$node_name" ]; then
  probe_host
  command -v kubectl >/dev/null 2>&1 || { echo "error: kubectl is required with --node" >&2; exit 1; }
  kubectl label node "$node_name" "$label_key=ready" --overwrite
  kubectl taint node "$node_name" "$label_key=ready:NoSchedule" --overwrite
fi

echo "installed runtime: $runtime_path"
echo "installed shim: $shim_path"
echo "installed containerd fragment: $fragment_path"
echo "ensure config.toml version=2 imports \"$config_dir/*.toml\", then restart containerd"
echo "install RuntimeClass with: kubectl apply -f $runtimeclass_path"
echo "verify with: $0 probe"
echo "rollback with: $0 uninstall${node_name:+ --node $node_name}"

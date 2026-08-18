#!/usr/bin/env bash
# Prove that a real containerd owns and administers a real platform-factory
# native-KVM MicroVM through platform-factory-shim's Sandbox+Task TTRPC contract,
# driven by crictl exactly the way a kubelet would. This requires
# Linux/amd64, /dev/kvm and root (a dedicated containerd instance, CNI); it
# deliberately has no contract-only fallback - see
# tests/kind/test-kind-runtimeclass.sh for the scheduling-only contract that
# runs without a real runtime or KVM.
set -euo pipefail

if [ "$#" -ne 4 ]; then
  echo "usage: $0 OCI_LAYOUT IMAGE KERNEL MICROVM_INIT" >&2
  exit 2
fi
if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
  echo "error: containerd/KVM proof requires Linux amd64" >&2
  exit 1
fi
test -r /dev/kvm

layout=$1
image=$2
kernel=$3
microvm_init=$4
evidence_dir=${PLATFORM_FACTORY_EVIDENCE_DIR:-.}
work=$(mktemp -d "${TMPDIR:-/tmp}/platform-factory-containerd-kvm.XXXXXX")
sock="$work/containerd.sock"
pod_name=platform-factory-containerd-e2e
# Computed up front, not where first used below: cleanup() references these
# under set -u, and must not fail on an unset variable if it fires from a
# trap before the script reaches the block that would otherwise set them.
invoking_uid=$(id -u)
invoking_gid=$(id -g)

for path in "$layout/index.json" "$kernel" "$microvm_init"; do
  test -r "$path"
done
for command in go sudo ctr crictl sha256sum tar; do
  command -v "$command" >/dev/null
done

cleanup() {
  # platform-factory-runtime create's own stdout/stderr are wired directly to the
  # container's CRI-managed stdio FIFOs (see task_service.go's
  # runtimeCreateCommand), not to this script's or containerd's own log
  # output; the only place a create-time failure's actual message ends up is
  # this log file. Capture it - and the bundle containerd generated - before
  # anything below deletes the work directory, on every exit path, not just
  # the success one, so a CI failure leaves evidence instead of a bare exit
  # code.
  # cp preserves the source's root-only owner and mode; the workflow's own
  # actions/upload-artifact step runs as the unprivileged runner user and
  # errors out reading a root-owned, non-world-readable file, which took
  # down the entire evidence upload the first time this ran. install -o/-g/-m
  # sets the copy's owner and mode explicitly instead of inheriting either
  # from the source, which containerd/the shim create as root regardless of
  # the [grpc] uid/gid the socket itself uses.
  sudo find "$work/logs" -type f \
    -exec sh -c 'install -o "'"$invoking_uid"'" -g "'"$invoking_gid"'" -m 0644 "$1" "'"$evidence_dir"'/containerd-microvm-container-$(basename "$1")"' _ {} \; \
    2>/dev/null || true
  sudo find "$work/state" -name config.json \
    -exec sh -c 'install -o "'"$invoking_uid"'" -g "'"$invoking_gid"'" -m 0644 "$1" "'"$evidence_dir"'/containerd-microvm-bundle-$(basename "$(dirname "$1")").json"' _ {} \; \
    2>/dev/null || true
  # platform-factory-shim invokes platform-factory-runtime as a plain OCI
  # runtime subprocess (create/start/... over argv, not a direct Go call
  # into internal/ociruntime), so its supervisor.log lands wherever that
  # binary's own defaultStateRoot() puts it - /run/user/0/platform-factory-runtime
  # here, since this whole script runs the shim as root via sudo - not
  # under $work/state at all. Same start-time-crash rationale as
  # test-podman-kvm.sh's identical capture.
  sudo find /run/user/*/platform-factory-runtime -maxdepth 1 -name '*.supervisor.log' \
    -exec sh -c 'install -o "'"$invoking_uid"'" -g "'"$invoking_gid"'" -m 0644 "$1" "'"$evidence_dir"'/containerd-microvm-$(basename "$1")"' _ {} \; \
    2>/dev/null || true
  crictl -r "unix://$sock" rmp -a -f >/dev/null 2>&1 || true
  sudo pkill -9 -f "containerd --config $work/containerd.toml" >/dev/null 2>&1 || true
  sudo ip link delete "$bridge_name" >/dev/null 2>&1 || true
  # A killed shim can leave the rootfs mount task_service.go's Create set up
  # behind (its own Delete, which unmounts it, never got to run); rm -rf on
  # a directory with something still mounted under it just fails.
  sudo awk '{print $2}' /proc/mounts | grep -F "$work/" | sort -r \
    | xargs -r -n1 sudo umount --lazy 2>/dev/null || true
  sudo rm -rf "$work"
}
trap cleanup EXIT

mkdir -p "$work/root" "$work/state" "$work/logs" "$work/cni.d"
# platform-factory-shim's sandbox never actually gives the guest the pod's network
# namespace (see sandbox_service.go) - the MicroVM has its own port
# forwarding, the same way test-podman-kvm.sh's "podman run --network none"
# needs no CNI at all. But containerd's CRI plugin still runs its own CNI
# setup before ever calling into the sandbox controller, and a loopback-only
# conf makes it fail sandbox creation outright ("failed to find network
# info"): the CRI plugin expects a CNI result carrying real network info
# (an IP), so a bridge + host-local IPAM plugin is required here even though
# nothing downstream of it ends up using that network.
for plugin in bridge host-local loopback; do
  command -v "/opt/cni/bin/$plugin" >/dev/null || {
    echo "error: CNI $plugin plugin not found at /opt/cni/bin (install containernetworking-plugins)" >&2
    exit 1
  }
done
bridge_name=secoci-e2e0
cat >"$work/cni.d/10-platform-factory-e2e.conflist" <<EOF
{
  "cniVersion": "1.0.0",
  "name": "platform-factory-e2e",
  "plugins": [
    {"type": "bridge", "bridge": "$bridge_name", "isGateway": true, "ipMasq": true,
     "ipam": {"type": "host-local", "subnet": "10.89.0.0/16", "routes": [{"dst": "0.0.0.0/0"}]}},
    {"type": "loopback"}
  ]
}
EOF

go build -trimpath -o "$work/platform-factory-runtime" ./cmd/platform-factory-runtime
go build -trimpath -o "$work/platform-factory-shim" ./plugins/containerd/cmd/platform-factory-shim
go build -trimpath -o "$work/platform-factory-containerd" ./plugins/containerd/cmd/platform-factory-containerd
sudo install -m 0755 "$work/platform-factory-runtime" /usr/local/bin/platform-factory-runtime
sudo install -m 0755 "$work/platform-factory-shim" /usr/local/bin/containerd-shim-platform-factory-v1
sudo install -m 0755 "$work/platform-factory-containerd" /usr/local/bin/platform-factory-containerd
"$work/platform-factory-containerd" config | tee "$evidence_dir/containerd-microvm-runtime.toml" >"$work/runtime.toml"

# [grpc] uid/gid make containerd chown the socket to this (unprivileged)
# invoking user (sys.GetLocalListener, called from cmd/containerd's own
# main.go) even though the daemon itself still runs as root - overlayfs
# and CNI genuinely need that. Every crictl/ctr call below can then talk to
# it directly: only the daemon's own launch, and reading files it created
# itself (state/, logs/, both still root-owned regardless of the socket),
# still need sudo.
cat >"$work/containerd.toml" <<EOF
version = 2
root = "$work/root"
state = "$work/state"
imports = ["$work/runtime.toml"]
[grpc]
  address = "$sock"
  uid = $invoking_uid
  gid = $invoking_gid
[plugins."io.containerd.grpc.v1.cri".cni]
  bin_dir = "/opt/cni/bin"
  conf_dir = "$work/cni.d"
EOF

# A deliberately narrow PATH, not the caller's own: platform-factory-shim
# resolves "platform-factory-runtime" (and containerd resolves
# "containerd-shim-platform-factory-v1") by bare name, and any earlier PATH entry
# shadowing the copy this
# script just built and installed to /usr/local/bin would silently run
# stale binaries under every process containerd spawns from here on.
runtime_path=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
sudo env PATH="$runtime_path" setsid containerd --config "$work/containerd.toml" \
  >"$evidence_dir/containerd-microvm-daemon.log" 2>&1 &
for _ in $(seq 1 30); do
  test -S "$sock" && break
  sleep 1
done
test -S "$sock"
ctr --address "$sock" --namespace k8s.io version >"$evidence_dir/containerd-microvm-version.txt"

archive="$work/layout.tar"
tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
  -C "$layout" -cf "$archive" .
ctr --address "$sock" --namespace k8s.io images import "$archive" \
  | tee "$evidence_dir/containerd-microvm-import.txt"
# containerd normalizes a plain NAME:TAG reference (no registry/namespace
# component, which is exactly what oci-builder produces) to
# docker.io/library/NAME:TAG for its own image store; container-config.json
# below must ask for that same qualified form or CreateContainer fails to
# resolve an image ctr itself just reported as present.
qualified_image="docker.io/library/${image}"
ctr --address "$sock" --namespace k8s.io images tag "$image" "$qualified_image" \
  >/dev/null

kernel_digest=sha256:$(sha256sum "$kernel" | awk '{print $1}')
init_digest=sha256:$(sha256sum "$microvm_init" | awk '{print $1}')
cat >"$work/pod-config.json" <<EOF
{
  "metadata": {"name": "$pod_name", "namespace": "default", "uid": "$pod_name-uid"},
  "log_directory": "$work/logs"
}
EOF
# Without an explicit apparmor profile, containerd's CRI plugin defaults an
# unset one to RuntimeDefault and generates a real AppArmor SpecOpts for it
# (internal/cri/sputil.GenerateApparmorSpecOpts) whenever AppArmor is enabled
# on the host - which the CI runner's stock Ubuntu image has enabled, unlike
# most local dev containers, so this only ever surfaced there.
# internal/ociruntime rejects any non-empty AppArmor/SELinux label
# (runtime.go: "LSM labels are not yet supported") since a MicroVM boundary
# makes host-level LSM confinement of the guest both meaningless and
# unimplemented; Unconfined is the one profile type CRI never turns into a
# SpecOpts at all (see the same function's `case SecurityProfile_Unconfined`),
# so it's what any real Pod using this runtime needs too.
# crictl's own config-file loader unmarshals this with plain encoding/json,
# not protojson, so the enum needs its numeric value (SecurityProfile_
# Unconfined = 1), not its string name - "Unconfined" fails client-side with
# "cannot unmarshal string into Go struct field ...profile_type".
cat >"$work/container-config.json" <<EOF
{
  "metadata": {"name": "example-service"},
  "image": {"image": "$qualified_image"},
  "log_path": "example-service.log",
  "linux": {"security_context": {
    "capabilities": {"drop_capabilities": ["ALL"]},
    "apparmor": {"profile_type": 1}
  }},
  "annotations": {
    "platform-factory.dev/kernel-path": "$kernel",
    "platform-factory.dev/kernel-digest": "$kernel_digest",
    "platform-factory.dev/init-path": "$microvm_init",
    "platform-factory.dev/init-digest": "$init_digest"
  }
}
EOF

# The CRI plugin's CNI conf syncer discovers conf_dir asynchronously after
# the gRPC socket already answers, so an immediate RunPodSandbox can
# intermittently fail sandbox network setup even though the lone loopback
# conf is already on disk; retry rather than guess at a log line to wait on.
pod_id=""
for _ in $(seq 1 15); do
  if pod_id=$(crictl -r "unix://$sock" runp --runtime platform-factory "$work/pod-config.json" \
    | tee "$evidence_dir/containerd-microvm-runp.txt"); then
    break
  fi
  pod_id=""
  sleep 1
done
[ -n "$pod_id" ]
container_id=$(crictl -r "unix://$sock" create "$pod_id" \
  "$work/container-config.json" "$work/pod-config.json" \
  | tee "$evidence_dir/containerd-microvm-create.txt")
# platform-factory-runtime's own boot can legitimately outrun crictl's short
# default per-call timeout (2s); the shim's Start handler does not tie the
# guest boot to that deadline (see task_service.go's runtimeCommand), so a
# generous client-side timeout here is just about not giving up on the
# *wait*, not about how long the boot itself is allowed to take.
crictl -r "unix://$sock" --timeout=60s start "$container_id" \
  | tee "$evidence_dir/containerd-microvm-start.txt"

log_path="$work/logs/example-service.log"
# $log_path only ever captures whatever platform-factory-runtime's own
# short-lived "create" invocation writes to its own stdout/stderr (see
# task_service.go's runtimeCreateCommand and this script's own cleanup()
# trap comment on it) - real create-time failure text, not the guest's
# own output. The guest's serial console (kernel boot log, and this
# service's own JSON log line) only ever reaches LaunchSupervisor's
# dedicated on-disk supervisor.log (internal/ociruntime/supervisor_linux.go),
# same as the podman/docker proofs - see test-podman-kvm.sh's identical
# check for why. Poll that instead of, not just in addition to, $log_path.
supervisor_log=""
find_supervisor_log() {
  for log in /run/user/*/platform-factory-runtime/"$container_id".supervisor.log; do
    sudo test -e "$log" || continue
    supervisor_log=$log
    return 0
  done
  return 1
}

for _ in $(seq 1 90); do
  crictl -r "unix://$sock" ps -a >"$evidence_dir/containerd-microvm-ps.txt" 2>&1 || true
  if { [ -n "$supervisor_log" ] || find_supervisor_log; } \
    && sudo grep -Fq '"component":"example-service"' "$supervisor_log"; then
    break
  fi
  sleep 1
done
# install -o/-g/-m, not cp: see the cleanup() trap's comment on why a plain
# cp of a root-owned log breaks actions/upload-artifact, which runs
# unprivileged.
sudo install -o "$invoking_uid" -g "$invoking_gid" -m 0644 \
  "$log_path" "$evidence_dir/containerd-microvm-logs.txt" 2>/dev/null || true
[ -n "$supervisor_log" ] && sudo install -o "$invoking_uid" -g "$invoking_gid" -m 0644 \
  "$supervisor_log" "$evidence_dir/containerd-microvm-supervisor.log" 2>/dev/null || true
[ -n "$supervisor_log" ]
sudo grep -Fq '"component":"example-service"' "$supervisor_log"
running_state=$(crictl -r "unix://$sock" inspect "$container_id" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"]["state"])')
[ "$running_state" = CONTAINER_RUNNING ]

# Exercise the terminal half of the same CRI lifecycle. stop must reach the
# guest through platform-factory-runtime kill, inspect must publish EXITED, and rm
# must delegate delete before containerd forgets the task.
crictl -r "unix://$sock" --timeout=60s stop --timeout 30 "$container_id" \
  | tee "$evidence_dir/containerd-microvm-stop.txt"
stopped_state=$(crictl -r "unix://$sock" inspect "$container_id" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"]["state"])')
[ "$stopped_state" = CONTAINER_EXITED ]
crictl -r "unix://$sock" --timeout=60s rm "$container_id" \
  | tee "$evidence_dir/containerd-microvm-rm.txt"
if crictl -r "unix://$sock" ps -a --id "$container_id" \
  | tail -n +2 | grep -Fq "$container_id"; then
  echo "error: removed MicroVM task is still visible in containerd" >&2
  exit 1
fi

printf 'CONTAINERD_MICROVM_E2E_OK pod=%s container=%s runtime=platform-factory-runtime vmm=kvm lifecycle=create,start,inspect,logs,stop,rm\n' \
  "$pod_id" "$container_id" | tee "$evidence_dir/containerd-microvm-result.txt"

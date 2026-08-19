#!/usr/bin/env bash
# Prove that Podman owns and administers a real platform-factory native-KVM
# MicroVM through the OCI runtime contract. This requires Linux/amd64 and
# /dev/kvm; it deliberately has no contract-only fallback.
set -euo pipefail

if [ "$#" -ne 4 ]; then
  echo "usage: $0 OCI_LAYOUT IMAGE KERNEL MICROVM_INIT" >&2
  exit 2
fi
if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
  echo "error: Podman/KVM proof requires Linux amd64" >&2
  exit 1
fi
test -r /dev/kvm

layout=$1
image=$2
kernel=$3
microvm_init=$4
evidence_dir=${PLATFORM_FACTORY_EVIDENCE_DIR:-.}
container_name=${PLATFORM_FACTORY_PODMAN_NAME:-secure-img}

for path in "$layout/index.json" "$kernel" "$microvm_init"; do
  test -r "$path"
done
for command in go podman sha256sum tar; do
  command -v "$command" >/dev/null
done

cleanup() {
  podman logs "$container_name" >"$evidence_dir/podman-microvm-logs-final.txt" 2>&1 || true
  podman inspect "$container_name" >"$evidence_dir/podman-microvm-inspect-final.json" 2>&1 || true
  # platform-factory-runtime's own supervisor.log is the only place a
  # start-time crash (e.g. "supervisor died before responding to start")
  # explains itself - runtime.go's error message points here, but nothing
  # else on this path (podman logs/inspect) ever sees it, since the
  # supervisor died before the guest could produce any container output.
  # Copy every one found rather than parsing the container ID out of podman's
  # own output, since exactly one container runs per invocation of this
  # script.
  for log in /run/user/*/platform-factory-runtime/*.supervisor.log \
    /tmp/podman-run-*/platform-factory-runtime/*.supervisor.log; do
    [ -e "$log" ] || continue
    cp "$log" "$evidence_dir/podman-microvm-$(basename "$log")" 2>/dev/null || true
  done
  podman rm --force "$container_name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

podman rm --force "$container_name" >/dev/null 2>&1 || true
scripts/microvm/install-podman-runtime.sh \
  | tee "$evidence_dir/podman-microvm-install.txt"
podman info --format json >"$evidence_dir/podman-microvm-info.json"

archive=$(mktemp "${TMPDIR:-/tmp}/platform-factory-podman-layout.XXXXXX.tar")
trap 'rm -f "$archive"; cleanup' EXIT
tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
  -C "$layout" -cf "$archive" .
podman load --input "$archive" | tee "$evidence_dir/podman-microvm-load.txt"
podman image exists "$image"

kernel_digest=sha256:$(sha256sum "$kernel" | awk '{print $1}')
init_digest=sha256:$(sha256sum "$microvm_init" | awk '{print $1}')
container_id=$(podman run --detach \
  --runtime platform-factory-runtime \
  --name "$container_name" \
  --network none \
  --cap-drop all \
  --annotation "platform-factory.dev/kernel-path=$kernel" \
  --annotation "platform-factory.dev/kernel-digest=$kernel_digest" \
  --annotation "platform-factory.dev/init-path=$microvm_init" \
  --annotation "platform-factory.dev/init-digest=$init_digest" \
  "$image" | tee "$evidence_dir/podman-microvm-run.txt")
echo "$container_id"

# `podman logs` can never show the guest's own output with this runtime:
# LaunchSupervisor (internal/ociruntime/supervisor_linux.go) deliberately
# redirects the supervisor process's stdout/stderr - which is where
# RunLinuxWithOptions's SerialWriter relays the guest's serial console,
# kernel boot log included - to its own dedicated on-disk supervisor.log,
# never to conmon's inherited stdio pipe (see that function's own
# SIGPIPE-avoidance comment for why). Poll the supervisor.log directly
# for the same evidence instead - this is not a workaround for a bug,
# it is the only place this information has ever existed.
#
# platform-factory-runtime resolves its own state root from
# XDG_RUNTIME_DIR, falling back to /run/user/<euid> - correct with a
# real systemd user session (every real Podman/CI host). Rootless
# Podman on a host with no such session (e.g. this dev sandbox)
# synthesizes and passes down its own ephemeral XDG_RUNTIME_DIR instead
# (typically /tmp/podman-run-<uid>), invisible to this script's own,
# separate environment - so check both rather than assuming either.
supervisor_log=""
find_supervisor_log() {
  for log in "/run/user/$(id -u)/platform-factory-runtime/$container_id.supervisor.log" \
    /tmp/podman-run-*/platform-factory-runtime/"$container_id".supervisor.log; do
    [ -e "$log" ] || continue
    supervisor_log=$log
    return 0
  done
  return 1
}

for _ in $(seq 1 90); do
  podman ps --filter "name=^${container_name}$" --format json \
    >"$evidence_dir/podman-microvm-ps.json"
  podman logs "$container_name" >"$evidence_dir/podman-microvm-logs.txt" 2>&1 || true
  if { [ -n "$supervisor_log" ] || find_supervisor_log; } \
    && grep -Fq '"component":"example-service"' "$supervisor_log"; then
    break
  fi
  sleep 1
done
running_name=$(podman ps --filter "name=^${container_name}$" --format '{{.Names}}')
[ "$running_name" = "$container_name" ]
[ -n "$supervisor_log" ]
grep -Fq '"component":"example-service"' "$supervisor_log"

podman stop --time 20 "$container_name" | tee "$evidence_dir/podman-microvm-stop.txt"
test "$(podman inspect --format '{{.State.Status}}' "$container_name")" = exited
podman rm "$container_name" | tee "$evidence_dir/podman-microvm-rm.txt"
if podman container exists "$container_name"; then
  echo "error: Podman retained $container_name after rm" >&2
  exit 1
fi
trap 'rm -f "$archive"' EXIT
printf 'PODMAN_MICROVM_E2E_OK name=%s runtime=platform-factory-runtime vmm=kvm\n' "$container_name" \
  | tee "$evidence_dir/podman-microvm-result.txt"

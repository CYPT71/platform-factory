#!/usr/bin/env bash
# Prove that Docker owns and administers a real platform-factory native-KVM
# MicroVM through the OCI runtime contract, the same proof
# test-podman-kvm.sh already gives for Podman. This requires Linux/amd64
# and /dev/kvm; it deliberately has no contract-only fallback.
#
# --annotation on `docker run` requires a Docker Engine new enough to pass
# OCI annotations through to the runtime (this mirrors Podman's own
# long-supported --annotation flag test-podman-kvm.sh already relies on) -
# unverified against a real Docker Engine version matrix as of this
# writing; if it is rejected on an older Docker, that is a real
# compatibility gap to document, not a bug in this script's approach.
set -euo pipefail

if [ "$#" -ne 4 ]; then
  echo "usage: $0 OCI_LAYOUT IMAGE KERNEL MICROVM_INIT" >&2
  exit 2
fi
if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
  echo "error: Docker/KVM proof requires Linux amd64" >&2
  exit 1
fi
test -r /dev/kvm

layout=$1
image=$2
kernel=$3
microvm_init=$4
evidence_dir=${PLATFORM_FACTORY_EVIDENCE_DIR:-.}
container_name=${PLATFORM_FACTORY_DOCKER_NAME:-secure-img}

for path in "$layout/index.json" "$kernel" "$microvm_init"; do
  test -r "$path"
done
for command in go docker sha256sum tar; do
  command -v "$command" >/dev/null
done

cleanup() {
  docker logs "$container_name" >"$evidence_dir/docker-microvm-logs-final.txt" 2>&1 || true
  docker inspect "$container_name" >"$evidence_dir/docker-microvm-inspect-final.json" 2>&1 || true
  # platform-factory-runtime's own supervisor.log is the only place a
  # start-time crash (e.g. "supervisor died before responding to start")
  # explains itself - see test-podman-kvm.sh's identical capture for why.
  # dockerd normally runs as root, so its store lives under /run/user/0;
  # sudo covers that without assuming which UID actually launched it.
  for log in /run/user/*/platform-factory-runtime/*.supervisor.log; do
    [ -e "$log" ] || continue
    sudo cp "$log" "$evidence_dir/docker-microvm-$(basename "$log")" 2>/dev/null || true
    sudo chown "$(id -u):$(id -g)" "$evidence_dir/docker-microvm-$(basename "$log")" 2>/dev/null || true
  done
  docker rm --force "$container_name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker rm --force "$container_name" >/dev/null 2>&1 || true
scripts/microvm/install-docker-runtime.sh \
  | tee "$evidence_dir/docker-microvm-install.txt"
echo "reloading dockerd to pick up the new runtime registration"
sudo systemctl reload docker
docker info --format '{{json .Runtimes}}' >"$evidence_dir/docker-microvm-info.json"

archive=$(mktemp "${TMPDIR:-/tmp}/platform-factory-docker-layout.XXXXXX.tar")
trap 'rm -f "$archive"; cleanup' EXIT
tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
  -C "$layout" -cf "$archive" .
docker load --input "$archive" | tee "$evidence_dir/docker-microvm-load.txt"
docker image inspect "$image" >/dev/null

kernel_digest=sha256:$(sha256sum "$kernel" | awk '{print $1}')
init_digest=sha256:$(sha256sum "$microvm_init" | awk '{print $1}')
container_id=$(docker run --detach \
  --runtime platform-factory-runtime \
  --name "$container_name" \
  --network none \
  --cap-drop all \
  --annotation "platform-factory.dev/kernel-path=$kernel" \
  --annotation "platform-factory.dev/kernel-digest=$kernel_digest" \
  --annotation "platform-factory.dev/init-path=$microvm_init" \
  --annotation "platform-factory.dev/init-digest=$init_digest" \
  "$image" | tee "$evidence_dir/docker-microvm-run.txt")
echo "$container_id"

# `docker logs` can never show the guest's own output with this runtime:
# LaunchSupervisor (internal/ociruntime/supervisor_linux.go) deliberately
# redirects the supervisor process's stdout/stderr - which is where
# RunLinuxWithOptions's SerialWriter relays the guest's serial console,
# kernel boot log included - to its own dedicated on-disk supervisor.log,
# never to conmon/containerd-shim's inherited stdio pipe (see that
# function's own SIGPIPE-avoidance comment for why). Poll the
# supervisor.log directly for the same evidence instead - this is not a
# workaround for a bug, it is the only place this information has ever
# existed. dockerd normally runs as root, so its store lives under
# /run/user/0, but sudo-globbing every /run/user/*/platform-factory-runtime
# (same glob the cleanup trap above already uses successfully) avoids
# hardcoding that assumption.
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
  docker ps --filter "name=^${container_name}\$" --format json \
    >"$evidence_dir/docker-microvm-ps.json"
  docker logs "$container_name" >"$evidence_dir/docker-microvm-logs.txt" 2>&1 || true
  if { [ -n "$supervisor_log" ] || find_supervisor_log; } \
    && sudo grep -Fq '"component":"example-service"' "$supervisor_log"; then
    break
  fi
  sleep 1
done
running_name=$(docker ps --filter "name=^${container_name}\$" --format '{{.Names}}')
[ "$running_name" = "$container_name" ]
[ -n "$supervisor_log" ]
sudo grep -Fq '"component":"example-service"' "$supervisor_log"

docker stop --time 20 "$container_name" | tee "$evidence_dir/docker-microvm-stop.txt"
test "$(docker inspect --format '{{.State.Status}}' "$container_name")" = exited
docker rm "$container_name" | tee "$evidence_dir/docker-microvm-rm.txt"
if docker inspect "$container_name" >/dev/null 2>&1; then
  echo "error: Docker retained $container_name after rm" >&2
  exit 1
fi
trap 'rm -f "$archive"' EXIT
printf 'DOCKER_MICROVM_E2E_OK name=%s runtime=platform-factory-runtime vmm=kvm\n' "$container_name" \
  | tee "$evidence_dir/docker-microvm-result.txt"

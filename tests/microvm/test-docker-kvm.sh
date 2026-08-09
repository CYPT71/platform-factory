#!/usr/bin/env bash
# Prove that a dedicated Docker Engine owns a real secure-oci native-KVM
# MicroVM through Docker's opt-in OCI runtime selection.
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
for command in docker dockerd go sha256sum; do
  command -v "$command" >/dev/null
done

layout=$1
image=$2
kernel=$3
microvm_init=$4
evidence_dir=${PLATFORM_FACTORY_EVIDENCE_DIR:-.}
container_name=${PLATFORM_FACTORY_DOCKER_NAME:-secure-img-docker}
crash_name="${container_name}-crash"
work=$(mktemp -d "${TMPDIR:-/tmp}/secure-oci-docker-kvm.XXXXXX")
sock="$work/docker.sock"
runtime="$work/platform-factory-runtime"
platform_factory="$work/platform-factory"
docker_host="unix://$sock"

cleanup() {
  docker --host "$docker_host" logs "$container_name" >"$evidence_dir/docker-microvm-logs-final.txt" 2>&1 || true
  docker --host "$docker_host" inspect "$container_name" >"$evidence_dir/docker-microvm-inspect-final.json" 2>&1 || true
  docker --host "$docker_host" rm --force "$container_name" >/dev/null 2>&1 || true
  docker --host "$docker_host" rm --force "$crash_name" >/dev/null 2>&1 || true
  if [ -s "$work/dockerd.pid" ]; then
    sudo kill "$(cat "$work/dockerd.pid")" >/dev/null 2>&1 || true
  fi
  sudo pkill -9 -f "dockerd.*--host=$docker_host" >/dev/null 2>&1 || true
  case "$work" in
    "${TMPDIR:-/tmp}"/secure-oci-docker-kvm.*) sudo rm -rf -- "$work" ;;
    *) echo "refusing to remove unexpected work directory: $work" >&2 ;;
  esac
}
trap cleanup EXIT

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
  -o "$runtime" ./cmd/platform-factory-runtime
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
  -o "$platform_factory" ./cmd/platform-factory
chmod 0755 "$runtime"

sudo env PATH="$(dirname "$runtime"):$PATH" dockerd \
  --host="$docker_host" \
  --data-root="$work/data" \
  --exec-root="$work/exec" \
  --pidfile="$work/dockerd.pid" \
  --bridge=none --iptables=false --ip-forward=false --ip-masq=false \
  --add-runtime="platform-factory-runtime=$runtime" \
  >"$evidence_dir/docker-microvm-daemon.log" 2>&1 &

for _ in $(seq 1 60); do
  if docker --host "$docker_host" info >"$evidence_dir/docker-microvm-info.txt" 2>&1; then
    break
  fi
  sleep 1
done
docker --host "$docker_host" info >/dev/null

DOCKER_HOST="$docker_host" "$platform_factory" import --runtime docker --layout "$layout" "$image" \
  | tee "$evidence_dir/docker-microvm-load.txt"
docker --host "$docker_host" image inspect "$image" >/dev/null

kernel_digest=sha256:$(sha256sum "$kernel" | awk '{print $1}')
init_digest=sha256:$(sha256sum "$microvm_init" | awk '{print $1}')
docker --host "$docker_host" run --detach \
  --runtime platform-factory-runtime \
  --name "$container_name" \
  --network none \
  --cap-drop all \
  --annotation "platform-factory.dev/kernel-path=$kernel" \
  --annotation "platform-factory.dev/kernel-digest=$kernel_digest" \
  --annotation "platform-factory.dev/init-path=$microvm_init" \
  --annotation "platform-factory.dev/init-digest=$init_digest" \
  "$image" | tee "$evidence_dir/docker-microvm-run.txt"

for _ in $(seq 1 90); do
  docker --host "$docker_host" ps --filter "name=^/${container_name}$" --format json \
    >"$evidence_dir/docker-microvm-ps.json"
  docker --host "$docker_host" logs "$container_name" >"$evidence_dir/docker-microvm-logs.txt" 2>&1 || true
  if grep -Fq '"component":"example-service"' "$evidence_dir/docker-microvm-logs.txt"; then
    break
  fi
  sleep 1
done
test "$(docker --host "$docker_host" inspect --format '{{.State.Status}}' "$container_name")" = running
grep -Fq '"component":"example-service"' "$evidence_dir/docker-microvm-logs.txt"

docker --host "$docker_host" stop --time 20 "$container_name" | tee "$evidence_dir/docker-microvm-stop.txt"
test "$(docker --host "$docker_host" inspect --format '{{.State.Status}}' "$container_name")" = exited
docker --host "$docker_host" rm "$container_name" | tee "$evidence_dir/docker-microvm-rm.txt"
if docker --host "$docker_host" container inspect "$container_name" >/dev/null 2>&1; then
  echo "error: Docker retained $container_name after rm" >&2
  exit 1
fi

# Failure proof: kill the process that owns the VM, then require Docker and
# the shared atomic state store to converge without a surviving PID or state.
crash_id=$(docker --host "$docker_host" run --detach \
  --runtime platform-factory-runtime \
  --name "$crash_name" \
  --network none \
  --cap-drop all \
  --annotation "platform-factory.dev/kernel-path=$kernel" \
  --annotation "platform-factory.dev/kernel-digest=$kernel_digest" \
  --annotation "platform-factory.dev/init-path=$microvm_init" \
  --annotation "platform-factory.dev/init-digest=$init_digest" \
  "$image")
for _ in $(seq 1 90); do
  crash_pid=$(docker --host "$docker_host" inspect --format '{{.State.Pid}}' "$crash_name")
  if [ "$crash_pid" -gt 0 ]; then
    break
  fi
  sleep 1
done
test "$crash_pid" -gt 0
state_root=$(sudo tr '\0' '\n' <"/proc/$crash_pid/cmdline" | awk 'previous == "--root" { print; exit } { previous = $0 }')
test -n "$state_root"
sudo kill -9 "$crash_pid"
for _ in $(seq 1 60); do
  crash_status=$(docker --host "$docker_host" inspect --format '{{.State.Status}}' "$crash_name")
  if [ "$crash_status" = exited ]; then
    break
  fi
  sleep 1
done
test "$crash_status" = exited
docker --host "$docker_host" inspect "$crash_name" >"$evidence_dir/docker-microvm-crash-inspect.json"
docker --host "$docker_host" rm "$crash_name" | tee "$evidence_dir/docker-microvm-crash-rm.txt"
if sudo kill -0 "$crash_pid" >/dev/null 2>&1; then
  echo "error: killed MicroVM supervisor PID $crash_pid survived reconciliation" >&2
  exit 1
fi
if sudo test -e "$state_root/$crash_id.json"; then
  echo "error: runtime state survived crash cleanup for $crash_id" >&2
  exit 1
fi
printf 'DOCKER_MICROVM_CRASH_RECOVERY_OK id=%s killed_pid=%s state=removed ports=none volumes=none\n' \
  "$crash_id" "$crash_pid" | tee "$evidence_dir/docker-microvm-crash-result.txt"
printf 'DOCKER_MICROVM_E2E_OK name=%s runtime=platform-factory-runtime vmm=kvm lifecycle=create,start,inspect,logs,stop,rm\n' "$container_name" \
  | tee "$evidence_dir/docker-microvm-result.txt"

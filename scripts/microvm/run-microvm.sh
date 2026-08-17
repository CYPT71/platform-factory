#!/usr/bin/env bash
# Boot an already-built OCI image layout under plain QEMU/KVM instead of
# a container - see the MicroVM Support wiki page for the full picture. Every run
# independently re-verifies OCI_IMAGE_DIR before trusting any of its
# content. Linux + KVM only; guest architecture must match the host's.
set -euo pipefail

log() {
  printf '[%s] %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*" >&2
}

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: $0 OCI_IMAGE_DIR [PORT]" >&2
  echo "  OCI_IMAGE_DIR  a layout built by cmd/oci-builder (has oci-layout, index.json, blobs/sha256/)" >&2
  echo "  PORT           host and guest TCP port to forward (default 8080)" >&2
  echo >&2
  echo "env overrides: MICROVM_KERNEL, MICROVM_MEMORY (default 128M), MICROVM_SMP (default 1)," >&2
  echo "               MICROVM_HOST_ADDRESS (127.0.0.1 by default; 0.0.0.0 for explicit container publication)," >&2
  echo "               MICROVM_QEMU_SANDBOX (default a locked-down -sandbox preset; empty to disable)," >&2
  echo "               MICROVM_SMOKE_TEST_PATH (if set, curl this path once and exit instead of staying up)," >&2
  echo "               MICROVM_SHUTDOWN_TEST_PATH (if set, POST this path once, then wait for the guest to" >&2
  echo "               power itself off and verify QEMU exits cleanly - a graceful-shutdown test, not a boot smoke test)," >&2
  echo "               MICROVM_BOOT_MANIFEST (a combined kernel+init+layer digest is always computed and logged;" >&2
  echo "               set this to also persist the manifest to a path outside the run's temp directory)" >&2
  exit 2
fi
if [ -n "${BASH_VERSION:-}" ]; then
  script_path=${BASH_SOURCE[0]}
elif [ -n "${ZSH_VERSION:-}" ]; then
  script_path=${(%):-%x}
else
  echo "error: this script requires Bash or Zsh" >&2
  exit 1
fi
image_dir=$1
port=${2:-8080}
host_address=${MICROVM_HOST_ADDRESS:-127.0.0.1}
case "$host_address" in
  127.0.0.1|0.0.0.0) ;;
  *) echo "error: MICROVM_HOST_ADDRESS must be 127.0.0.1 or 0.0.0.0" >&2; exit 2 ;;
esac
forward_rules=${MICROVM_FORWARDS:-}

repo_root=$(cd "$(dirname "$script_path")/../.." && pwd)
script_dir="$repo_root/scripts/microvm"
# shellcheck source=scripts/microvm/lib-arch.sh
. "$script_dir/lib-arch.sh"

log "phase=preflight checking KVM and required tools"
"$script_dir/check-kvm.sh"

for cmd in go python3 curl cpio; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "error: '$cmd' is required on PATH" >&2; exit 1; }
done

# Not optional, not skippable: never trust a layout we haven't verified.
log "phase=verify-layout path=$image_dir"
python3 "$repo_root/scripts/ci/verify-oci-layout.py" "$image_dir"

log "phase=read-config reading OCI image metadata"
image_meta=$(python3 -c "
import json, sys

image_dir = sys.argv[1]
idx = json.load(open(f'{image_dir}/index.json'))
manifest_digest = idx['manifests'][0]['digest'].split(':', 1)[1]
manifest = json.load(open(f'{image_dir}/blobs/sha256/{manifest_digest}'))

if len(manifest['layers']) != 1:
    sys.exit(f'expected exactly one layer, found {len(manifest[\"layers\"])}')
layer_digest = manifest['layers'][0]['digest'].split(':', 1)[1]

config_digest = manifest['config']['digest'].split(':', 1)[1]
config = json.load(open(f'{image_dir}/blobs/sha256/{config_digest}'))
entrypoint = config['config'].get('Entrypoint') or []
if not entrypoint:
    sys.exit('image config has no Entrypoint')

print(config['architecture'])
print(layer_digest)
for part in entrypoint:
    print(part)
" "$image_dir")

image_arch=$(sed -n '1p' <<<"$image_meta")
layer_digest=$(sed -n '2p' <<<"$image_meta")
entrypoint=()
while IFS= read -r part; do entrypoint+=("$part"); done < <(sed -n '3,$p' <<<"$image_meta")

if [ "$image_arch" != "$HOST_ARCH" ]; then
  echo "error: image architecture is '$image_arch' but the host is '$HOST_ARCH' - KVM cannot accelerate a cross-architecture guest" >&2
  exit 1
fi
log "phase=read-config arch=$image_arch entrypoint=${entrypoint[*]}"

work=$(mktemp -d "${TMPDIR:-/tmp}/platform-factory-base-microvm.XXXXXX")
qemu_pid=""
tail_pid=""
cleanup() {
  [ -n "$tail_pid" ] && kill "$tail_pid" >/dev/null 2>&1 || true
  [ -n "$qemu_pid" ] && kill "$qemu_pid" >/dev/null 2>&1 || true
  [ -n "$qemu_pid" ] && wait "$qemu_pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

log "phase=build-init architecture=$HOST_ARCH"
CGO_ENABLED=0 GOOS=linux GOARCH="$HOST_ARCH" go build -trimpath -ldflags='-s -w' \
  -o "$work/init" "$repo_root/cmd/microvm-init"
log "phase=build-init complete bytes=$(wc -c < "$work/init")"

log "phase=initramfs extracting verified layer and assembling guest filesystem"
"$script_dir/assemble-initramfs.sh" \
  "$image_dir/blobs/sha256/$layer_digest" "$work/init" "$work/initramfs.cpio.gz" \
  "${entrypoint[@]}"
log "phase=initramfs complete bytes=$(wc -c < "$work/initramfs.cpio.gz")"

kernel="${MICROVM_KERNEL:-$repo_root/.cache/microvm/$HOST_ARCH/kernel}"
log "phase=kernel ensuring kernel path=$kernel"
"$script_dir/build-kernel.sh" "$HOST_ARCH" "$kernel"
log "phase=kernel ready bytes=$(wc -c < "$kernel")"

# Always computed, not opt-in: every boot gets one digest covering every
# component actually used (kernel, init, OCI layer, entrypoint), even though
# the kernel and initramfs are never part of the OCI manifest itself. Only
# persisted outside $work when MICROVM_BOOT_MANIFEST names a path.
boot_manifest="${MICROVM_BOOT_MANIFEST:-$work/boot-manifest.json}"
log "phase=boot-manifest computing combined digest path=$boot_manifest"
kernel_provenance="$(dirname "$kernel")/kernel.provenance.json"
python3 "$repo_root/scripts/ci/write-microvm-boot-manifest.py" \
  --architecture "$image_arch" \
  --layer-digest "sha256:$layer_digest" \
  --init "$work/init" \
  --kernel "$kernel" \
  --kernel-provenance "$kernel_provenance" \
  --output "$boot_manifest" \
  -- "${entrypoint[@]}"
combined_digest=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["combined_digest"])' "$boot_manifest")
log "phase=boot-manifest complete combined_digest=$combined_digest"

case "$HOST_ARCH" in
  amd64)
    machine_args=(-machine microvm,pcie=on,acpi=on)
    console_dev=ttyS0
    ;;
  arm64)
    machine_args=(-machine virt)
    console_dev=ttyAMA0
    ;;
esac

qemu_args=(
  "${machine_args[@]}"
  -enable-kvm -cpu host
  -m "${MICROVM_MEMORY:-128M}" -smp "${MICROVM_SMP:-1}"
  -kernel "$kernel"
  -initrd "$work/initramfs.cpio.gz"
  # `--` marks everything after it as argv for /sbin/init (kernel convention).
  -append "console=$console_dev rdinit=/sbin/init ip=10.0.2.15::10.0.2.2:255.255.255.0::eth0:off panic=-1 -- ${entrypoint[*]}"
  -netdev "user,id=n0"
  -device virtio-net-pci,netdev=n0
  -no-reboot -nographic -no-user-config -monitor none
)
netdev="user,id=n0"
probe_port=""
if [ -z "$forward_rules" ]; then
  forward_rules="tcp|${host_address}|${port}|${port}"
fi
IFS=';' read -r -a forwards <<< "$forward_rules"
for rule in "${forwards[@]}"; do
  IFS='|' read -r protocol forward_address host_port guest_port extra <<< "$rule"
  if [ -n "${extra:-}" ] || { [ "$protocol" != tcp ] && [ "$protocol" != udp ]; } ||
    ! [[ "$host_port" =~ ^[0-9]+$ ]] || ! [[ "$guest_port" =~ ^[0-9]+$ ]] ||
    [ "$host_port" -lt 1 ] || [ "$host_port" -gt 65535 ] ||
    [ "$guest_port" -lt 1 ] || [ "$guest_port" -gt 65535 ]; then
    echo "error: invalid MICROVM_FORWARDS rule: $rule" >&2
    exit 2
  fi
  python3 -c 'import ipaddress,sys; ipaddress.ip_address(sys.argv[1])' "$forward_address" ||
    { echo "error: invalid forward address: $forward_address" >&2; exit 2; }
  if [[ "$forward_address" == *:* ]]; then
    forward_address="[$forward_address]"
  fi
  netdev+=",hostfwd=${protocol}:${forward_address}:${host_port}-:${guest_port}"
  if [ "$protocol" = tcp ] && [ -z "$probe_port" ]; then
    probe_port=$host_port
  fi
done
for index in "${!qemu_args[@]}"; do
  if [ "${qemu_args[$index]}" = "user,id=n0" ]; then
    qemu_args[$index]=$netdev
    break
  fi
done
sandbox="${MICROVM_QEMU_SANDBOX-on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny}"
if [ -n "$sandbox" ]; then
  qemu_args+=(-sandbox "$sandbox")
fi

log "phase=qemu launching binary=$QEMU_BIN architecture=$HOST_ARCH memory=${MICROVM_MEMORY:-128M} smp=${MICROVM_SMP:-1}"
console_log="$work/console.log"
"$QEMU_BIN" "${qemu_args[@]}" > "$console_log" 2>&1 &
qemu_pid=$!
tail -n +1 -f "$console_log" &
tail_pid=$!

ready=false
if [ -z "$probe_port" ]; then
  log "phase=wait-network skipped reason=no-tcp-forward"
  ready=true
else
  port=$probe_port
  log "phase=wait-network target=127.0.0.1:$port listen=$host_address attempts=60"
fi
for attempt in $(seq 1 60); do
  [ "$ready" = true ] && break
  if ! kill -0 "$qemu_pid" 2>/dev/null; then
    echo "error: qemu exited early; console log:" >&2
    cat "$console_log" >&2 || true
    exit 1
  fi
  if timeout 1 bash -c "exec 3<>\"/dev/tcp/127.0.0.1/$port\"" 2>/dev/null; then
    exec 3<&- 3>&- 2>/dev/null || true
    ready=true
    break
  fi
  if [ $((attempt % 5)) -eq 0 ]; then
    log "phase=wait-network attempt=$attempt/60 qemu_pid=$qemu_pid still_running=true"
  fi
  sleep 0.5
done

if [ "$ready" != true ]; then
  echo "error: guest never became reachable on port $port; console log:" >&2
  cat "$console_log" >&2 || true
  exit 1
fi
log "phase=wait-network ready target=127.0.0.1:$port"

if [ -n "${MICROVM_SMOKE_TEST_PATH:-}" ]; then
  log "phase=smoke-test request=http://127.0.0.1:${port}${MICROVM_SMOKE_TEST_PATH}"
  curl --fail --silent --show-error --connect-timeout 2 --max-time 10 \
    "http://127.0.0.1:${port}${MICROVM_SMOKE_TEST_PATH}"
  echo
  log "phase=smoke-test result=success"
  exit 0
fi

if [ -n "${MICROVM_SHUTDOWN_TEST_PATH:-}" ]; then
  log "phase=shutdown-test request=http://127.0.0.1:${port}${MICROVM_SHUTDOWN_TEST_PATH}"
  curl --silent --output /dev/null --show-error --connect-timeout 2 --max-time 10 \
    -X POST "http://127.0.0.1:${port}${MICROVM_SHUTDOWN_TEST_PATH}"
  log "phase=shutdown-test waiting for the guest to power itself off, attempts=30"
  for attempt in $(seq 1 30); do
    if ! kill -0 "$qemu_pid" 2>/dev/null; then
      qemu_exit=0
      wait "$qemu_pid" || qemu_exit=$?
      qemu_pid=""
      if [ "$qemu_exit" -ne 0 ]; then
        echo "error: qemu exited with status $qemu_exit instead of a clean guest-initiated poweroff; console log:" >&2
        cat "$console_log" >&2 || true
        exit 1
      fi
      if ! grep -q 'component=microvm-init.*action=poweroff' "$console_log"; then
        echo "error: qemu exited cleanly but microvm-init's poweroff log line is missing from the console; console log:" >&2
        cat "$console_log" >&2 || true
        exit 1
      fi
      log "phase=shutdown-test result=success qemu_exit=0"
      exit 0
    fi
    sleep 0.5
  done
  echo "error: guest did not power itself off within 15s of the shutdown request; console log:" >&2
  cat "$console_log" >&2 || true
  exit 1
fi

log "phase=running microVM is running; press Ctrl-C to stop it console=$console_log"
wait "$qemu_pid"

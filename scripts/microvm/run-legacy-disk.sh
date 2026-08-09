#!/usr/bin/env bash
# Boot a legacy VM disk image (RAW/QCOW2/VMDK/VHD/VHDX/ISO) under QEMU's own
# firmware (SeaBIOS/OVMF), executing the disk's own bootloader and kernel -
# unlike run-microvm.sh, which always boots this project's own kernel and
# initramfs directly. See docs/legacy-vm-disk-boot.md for the trust-model
# difference this implies: the guest kernel/bootloader here is untrusted,
# disk-supplied code, not something this project built and vetted.
#
# Accepts one or more DISK_IMAGE FORMAT pairs, for a project spanning
# multiple disks (an OS disk plus one or more data disks). The FIRST pair
# is always the boot disk; the rest are attached as additional, non-boot
# virtio-blk drives in the order given. Deciding WHICH disk is first is
# internal/vmdisk's job (vmdisk.SelectBootDisk) - this script trusts the
# order it is called with and never re-derives it.
#
# Every FORMAT argument is trusted as given - this script never sniffs any
# disk itself; format identification lives in internal/vmdisk (Go), one
# place, not duplicated here. Callers (normally `platform-factory microvm
# run-legacy-disk`) are expected to have called vmdisk.Detect/SelectBootDisk
# first and fail closed before ever invoking this script.
#
# No source disk is ever opened for writing: every non-ISO disk gets a
# disposable qcow2 overlay backed by the source, discarded on exit; ISO
# disks attach directly as read-only optical media (inherently read-only
# to the guest, so no overlay is needed). See "Ne jamais modifier une
# image disque source par défaut" in Meine-Graal's non-negotiable
# principles.
set -euo pipefail

log() {
  printf '[%s] %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*" >&2
}

usage() {
  echo "usage: $0 DISK_IMAGE FORMAT [DISK_IMAGE FORMAT ...]" >&2
  echo "  DISK_IMAGE  path to a legacy VM disk (never opened for writing)" >&2
  echo "  FORMAT      one of: raw qcow2 vmdk vhd vhdx iso" >&2
  echo "  The first pair is the boot disk; any further pairs are attached as" >&2
  echo "  additional, non-boot drives (a multi-disk project)." >&2
  echo >&2
  echo "env overrides: MICROVM_MEMORY (default 512M), MICROVM_SMP (default 1)," >&2
  echo "               MICROVM_FORWARDS (empty by default - this boot mode is" >&2
  echo "               network-isolated unless you opt in explicitly)," >&2
  echo "               MICROVM_QEMU_SANDBOX (default a locked-down -sandbox preset; empty to disable)," >&2
  echo "               MICROVM_LEGACY_BOOT_TIMEOUT (if set, run for this many seconds then stop and" >&2
  echo "               report the console log, instead of staying attached until Ctrl-C - this is a" >&2
  echo "               'did it survive boot' smoke check, not a readiness probe: this script has no" >&2
  echo "               way to know what, if anything, the guest actually serves)" >&2
  exit 2
}

if [ "$#" -lt 2 ] || [ "$(($# % 2))" -ne 0 ]; then
  usage
fi
if [ -n "${BASH_VERSION:-}" ]; then
  script_path=${BASH_SOURCE[0]}
elif [ -n "${ZSH_VERSION:-}" ]; then
  script_path=${(%):-%x}
else
  echo "error: this script requires Bash or Zsh" >&2
  exit 1
fi

disk_images=()
disk_formats=()
while [ "$#" -gt 0 ]; do
  disk_image=$1
  format=$2
  shift 2
  case "$format" in
    raw|qcow2|vmdk|vhd|vhdx|iso) ;;
    *) echo "error: unsupported FORMAT '$format' for '$disk_image' (expected one of: raw qcow2 vmdk vhd vhdx iso)" >&2; exit 2 ;;
  esac
  if [ ! -f "$disk_image" ]; then
    echo "error: '$disk_image' does not exist or is not a regular file" >&2
    exit 2
  fi
  disk_images+=("$disk_image")
  disk_formats+=("$format")
done

repo_root=$(cd "$(dirname "$script_path")/../.." && pwd)
script_dir="$repo_root/scripts/microvm"
# shellcheck source=scripts/microvm/lib-arch.sh
. "$script_dir/lib-arch.sh"

log "phase=preflight checking KVM and required tools"
"$script_dir/check-kvm.sh" # also checks for cpio, unused here but harmless to require
for cmd in "$QEMU_BIN" qemu-img; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "error: '$cmd' is required on PATH" >&2; exit 1; }
done
if [ "$HOST_ARCH" != amd64 ]; then
  echo "error: legacy-disk BIOS boot is only wired up for amd64 hosts today (q35 + SeaBIOS/OVMF)" >&2
  exit 1
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/platform-factory-legacy-disk.XXXXXX")
qemu_pid=""
tail_pid=""
cleanup() {
  [ -n "$tail_pid" ] && kill "$tail_pid" >/dev/null 2>&1 || true
  [ -n "$qemu_pid" ] && kill "$qemu_pid" >/dev/null 2>&1 || true
  [ -n "$qemu_pid" ] && wait "$qemu_pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

# The boot disk (index 0) determines boot order: hard disk unless it is
# itself an ISO. Every disk (boot or not) gets its own drive; order on
# the QEMU command line is preserved, and SeaBIOS/OVMF enumerate
# hard-disk boot candidates in that same order, so the boot disk stays
# first regardless of how many secondary disks follow it.
boot_order=c
if [ "${disk_formats[0]}" = iso ]; then
  boot_order=d
fi

drive_args=()
for i in "${!disk_images[@]}"; do
  disk_image=${disk_images[$i]}
  format=${disk_formats[$i]}
  if [ "$format" = iso ]; then
    drive_args+=(-drive "file=$disk_image,format=raw,if=ide,media=cdrom,readonly=on")
  else
    log "phase=overlay disk=$disk_image creating a disposable qcow2 overlay backed by the source (source is never opened for writing)"
    overlay="$work/overlay-$i.qcow2"
    qemu-img create -q -f qcow2 -b "$disk_image" -F "$format" "$overlay"
    drive_args+=(-drive "file=$overlay,format=qcow2,if=virtio")
  fi
done

qemu_args=(
  -machine q35 -enable-kvm -cpu host
  -m "${MICROVM_MEMORY:-512M}" -smp "${MICROVM_SMP:-1}"
  "${drive_args[@]}"
  -boot "order=$boot_order,menu=off"
  -no-reboot -nographic -no-user-config -monitor none -vga none
)

netdev=""
forward_rules=${MICROVM_FORWARDS:-}
if [ -z "$forward_rules" ]; then
  log "phase=network isolated (no -nic) - set MICROVM_FORWARDS to opt into forwarding"
  qemu_args+=(-nic none)
else
  netdev="user,id=n0"
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
  done
  qemu_args+=(-netdev "$netdev" -device virtio-net-pci,netdev=n0)
  log "phase=network forwarding rules=$forward_rules"
fi

sandbox="${MICROVM_QEMU_SANDBOX-on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny}"
if [ -n "$sandbox" ]; then
  qemu_args+=(-sandbox "$sandbox")
fi

log "phase=qemu launching binary=$QEMU_BIN disks=${#disk_images[@]} boot_disk=${disk_images[0]} boot_format=${disk_formats[0]} memory=${MICROVM_MEMORY:-512M} smp=${MICROVM_SMP:-1}"
console_log="$work/console.log"
"$QEMU_BIN" "${qemu_args[@]}" > "$console_log" 2>&1 &
qemu_pid=$!
tail -n +1 -f "$console_log" &
tail_pid=$!

if [ -n "${MICROVM_LEGACY_BOOT_TIMEOUT:-}" ]; then
  log "phase=boot-smoke-test waiting up to ${MICROVM_LEGACY_BOOT_TIMEOUT}s to confirm the guest is still running"
  slept=0
  while [ "$slept" -lt "$MICROVM_LEGACY_BOOT_TIMEOUT" ]; do
    if ! kill -0 "$qemu_pid" 2>/dev/null; then
      qemu_exit=0
      wait "$qemu_pid" || qemu_exit=$?
      qemu_pid=""
      echo "error: qemu exited (status $qemu_exit) before the boot-smoke-test window elapsed; console log:" >&2
      cat "$console_log" >&2 || true
      exit 1
    fi
    sleep 1
    slept=$((slept + 1))
  done
  log "phase=boot-smoke-test result=success qemu_pid=$qemu_pid still_running=true (this only proves QEMU did not crash - it does not prove the guest OS reached a usable state)"
  exit 0
fi

log "phase=running legacy disk boot is running; press Ctrl-C to stop it console=$console_log"
wait "$qemu_pid"

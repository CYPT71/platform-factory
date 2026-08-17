#!/usr/bin/env bash
# Non-destructive check: is this host ready to run scripts/microvm/run-microvm.sh?
# Never installs anything and never needs sudo. See the MicroVM Support wiki page for the
# install procedure when a check below fails. Works on both amd64 and
# arm64 hosts - KVM only accelerates a guest matching the host architecture.
set -euo pipefail

if [ -n "${BASH_VERSION:-}" ]; then
  script_path=${BASH_SOURCE[0]}
elif [ -n "${ZSH_VERSION:-}" ]; then
  script_path=${(%):-%x}
else
  echo "error: this script requires Bash or Zsh" >&2
  exit 1
fi
script_dir=$(cd "$(dirname "$script_path")" && pwd)
# shellcheck source=scripts/microvm/lib-arch.sh
. "$script_dir/lib-arch.sh"

fail=0
note() { printf '%s\n' "$1" >&2; }
ok()   { printf 'OK   %s\n' "$1"; }
bad()  { printf 'FAIL %s\n' "$1"; fail=1; }

if [ "$(uname -s)" != Linux ]; then
  bad "host OS is $(uname -s), not Linux (KVM is a Linux-only feature)"
else
  ok "host OS is Linux"
fi

if [ -z "$HOST_ARCH" ]; then
  bad "host CPU architecture is $(uname -m) - this tooling supports amd64 (x86_64) and arm64 (aarch64) hosts only"
else
  ok "host CPU architecture is $(uname -m) ($HOST_ARCH guests)"
fi

if [ "$HOST_ARCH" = amd64 ]; then
  if [ -r /proc/cpuinfo ] && grep -Eq '(vmx|svm)' /proc/cpuinfo; then
    ok "CPU exposes hardware virtualization (vmx/svm)"
  else
    bad "CPU does not expose vmx/svm in /proc/cpuinfo - hardware virtualization is unavailable or disabled in firmware"
  fi
elif [ "$HOST_ARCH" = arm64 ]; then
  # There is no single standard /proc/cpuinfo flag for EL2 (hypervisor
  # mode) support across arm64 vendors; /dev/kvm's existence below is the
  # authoritative check on this architecture.
  note "note: arm64 has no universal /proc/cpuinfo virtualization flag; relying on /dev/kvm below"
fi

if [ -e /dev/kvm ]; then
  ok "/dev/kvm exists"
  if [ -r /dev/kvm ] && [ -w /dev/kvm ]; then
    ok "/dev/kvm is readable/writable by $(id -un)"
  else
    bad "/dev/kvm exists but is not readable/writable by $(id -un) - add this user to the 'kvm' group (see the MicroVM Support wiki page) and start a new login session"
  fi
else
  bad "/dev/kvm does not exist - the kvm kernel module is not loaded, virtualization is disabled in firmware, or (arm64) EL2 is unavailable (see the MicroVM Support wiki page)"
fi

if [ -n "$QEMU_BIN" ]; then
  if command -v "$QEMU_BIN" >/dev/null 2>&1; then
    ok "'$QEMU_BIN' is on PATH"
  else
    bad "'$QEMU_BIN' is not on PATH (see the MicroVM Support wiki page)"
  fi
fi

if command -v cpio >/dev/null 2>&1; then
  ok "'cpio' is on PATH"
else
  bad "'cpio' is not on PATH (see the MicroVM Support wiki page)"
fi

if [ "$fail" -ne 0 ]; then
  note ""
  note "one or more checks failed; see https://github.com/CYPT71/platform-factory/wiki/MicroVM-Support for the install procedure."
  exit 1
fi
echo "host is ready for scripts/microvm/run-microvm.sh ($HOST_ARCH)"

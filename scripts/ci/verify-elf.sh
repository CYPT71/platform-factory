#!/usr/bin/env bash
# Verify that a release binary is a static ELF for an explicitly supported OCI architecture.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <binary> <amd64|arm64>" >&2
  exit 2
fi
binary=$1
arch=$2
[ -f "$binary" ] || { echo "ELF_VALIDATION_FAILURE: missing binary: $binary" >&2; exit 1; }
[ -x "$binary" ] || { echo "ELF_VALIDATION_FAILURE: non-executable binary: $binary" >&2; exit 1; }
header=$(readelf --file-header --wide "$binary")
printf '%s\n' "$header" | grep -q 'Class:[[:space:]]*ELF64' || { echo 'ELF_VALIDATION_FAILURE: expected ELF64' >&2; exit 1; }
case "$arch" in
  amd64) printf '%s\n' "$header" | grep -Eq 'Machine:[[:space:]]*(Advanced Micro Devices X86-64|AMD x86-64)' ;;
  arm64) printf '%s\n' "$header" | grep -Eq 'Machine:[[:space:]]*AArch64' ;;
  *) echo "ELF_VALIDATION_FAILURE: unsupported architecture: $arch" >&2; exit 2 ;;
esac || { echo "ELF_VALIDATION_FAILURE: architecture does not match $arch" >&2; exit 1; }
if readelf --program-headers --wide "$binary" | grep -q 'INTERP'; then
  echo 'ELF_VALIDATION_FAILURE: dynamically linked executable (PT_INTERP present)' >&2
  exit 1
fi
printf 'ELF_VALIDATION_OK binary=%s architecture=%s static=true\n' "$binary" "$arch"

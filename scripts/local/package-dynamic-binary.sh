#!/usr/bin/env bash
# Resolve a dynamically-linked binary's dependencies with ldd and package
# it into an OCI layout via cmd/oci-builder, one -extra-file per
# dependency. Must run on Linux matching the binary's own architecture -
# ldd traces it (e.g. inside `podman run --arch <arch> ...`).
set -euo pipefail

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
  echo "usage: $0 BINARY OUTPUT [ENTRYPOINT]" >&2
  echo "  BINARY      a dynamically-linked (or static) Linux ELF executable" >&2
  echo "  OUTPUT      new OCI layout directory (must not already exist)" >&2
  echo "  ENTRYPOINT  absolute container path for the binary (default /app/<basename>)" >&2
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

binary=$1
output=$2
entrypoint=${3:-/app/$(basename "$1")}

if [ "$(uname -s)" != Linux ]; then
  echo "error: this script must run on Linux (ldd traces the binary's own architecture) - see the container/VM note above" >&2
  exit 1
fi
for cmd in ldd go awk; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "error: '$cmd' is required on PATH" >&2; exit 1; }
done
[ -f "$binary" ] || { echo "error: '$binary' does not exist" >&2; exit 1; }

repo_root=$(cd "$(dirname "$script_path")/../.." && pwd)

echo "resolving dynamic dependencies of $binary (ldd)..." >&2
extra_file_args=()
interp_found=0
profile=static
while IFS= read -r line; do
  if [[ "$line" == *"=>"* ]]; then
    dep_path=$(awk '{print $3}' <<<"$line")
  else
    dep_path=$(awk '{print $1}' <<<"$line")
  fi
  [ -z "$dep_path" ] && continue
  if [ "$dep_path" = not ]; then
    echo "error: unresolved dynamic dependency: $line" >&2
    exit 1
  fi
  # linux-vdso.so.1 and similar have no backing file - skip them.
  [ -f "$dep_path" ] || continue
  case "$dep_path" in
    */ld-linux*.so*) interp_found=1; profile=glibc ;;
    */ld-musl*.so*|*/ld-*.so.*) interp_found=1; profile=musl ;;
  esac
  # Same path on both sides: the guest's linker expects libraries at
  # their normal system paths.
  extra_file_args+=(-extra-file "$dep_path=$dep_path")
done < <(ldd "$binary")

if [ "$interp_found" -eq 0 ]; then
  echo "note: no dynamic linker (ld-linux*) found in ldd output - '$binary' may be statically linked" >&2
fi

echo "building the OCI image layout ($((${#extra_file_args[@]} / 2)) extra file(s))..." >&2
(
  cd "$repo_root"
  go run ./cmd/oci-builder -binary "$binary" -output "$output" \
    -entrypoint "$entrypoint" -profile "$profile" "${extra_file_args[@]}"
)

echo "done: $output (entrypoint $entrypoint)" >&2

#!/usr/bin/env bash
# Locate a dotnet install root that carries a specific Microsoft.NETCore.App
# major version, independent of which `dotnet` (if any) resolves an SDK.
#
# On most machines the SDK-capable `dotnet` on PATH also carries every
# runtime a project needs, and this is a no-op: publish and execution both
# just use that `dotnet`. It only matters when the SDK and the runtime a
# project targets are split across separate installs - for example an SDK
# under /usr/share/dotnet that only has the newest runtimes, alongside an
# apt-installed runtime-only package under /usr/lib/dotnet. Do not prepend
# a runtime-only root to PATH to work around that: it would shadow the
# SDK-capable `dotnet` and break `dotnet publish`/`restore`/etc.
#
# Usage (sourced, for the function only):
#   source scripts/ci/dotnet-runtime.sh
#   root=$(find_dotnet_runtime_root 8) || { echo "missing runtime" >&2; exit 1; }
#
# Usage (executed directly, prints the root or fails with no output):
#   root=$(scripts/ci/dotnet-runtime.sh 8) || {
#     echo "C# conformance requires Microsoft.NETCore.App 8.x" >&2
#     exit 1
#   }
set -euo pipefail

find_dotnet_runtime_root() {
  local major=$1
  local candidate

  if command -v dotnet >/dev/null 2>&1; then
    # `command -v` can return a symlink (e.g. a devcontainer/user PATH
    # entry pointing at the real install, such as ~/.dotnet/dotnet ->
    # /usr/share/dotnet/dotnet). dirname on the symlink itself gives a
    # directory with no shared/Microsoft.NETCore.App tree of its own, so
    # resolve to the real path first - otherwise DOTNET_ROOT ends up
    # pointing nowhere useful even though the runtime check below (which
    # execs through the symlink and so always resolves correctly) passes.
    candidate=$(dirname -- "$(readlink -f -- "$(command -v dotnet)")")
    if "$candidate/dotnet" --list-runtimes 2>/dev/null | grep -q "^Microsoft\.NETCore\.App ${major}\."; then
      printf '%s\n' "$candidate"
      return 0
    fi
  fi

  # Known secondary roots a runtime-only install can live under, split
  # from the SDK. Each is only trusted after actually probing it for the
  # required runtime - never assumed to exist or to be correct.
  for candidate in "${DOTNET_ROOT:-}" /usr/lib/dotnet /usr/share/dotnet; do
    if [ -z "$candidate" ] || [ ! -x "$candidate/dotnet" ]; then
      continue
    fi
    if "$candidate/dotnet" --list-runtimes 2>/dev/null | grep -q "^Microsoft\.NETCore\.App ${major}\."; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done

  return 1
}

if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  if [ "$#" -ne 1 ]; then
    echo "usage: $0 <major-version>" >&2
    exit 2
  fi
  find_dotnet_runtime_root "$1"
fi

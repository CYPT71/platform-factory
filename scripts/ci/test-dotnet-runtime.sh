#!/usr/bin/env bash
# Exercises scripts/ci/dotnet-runtime.sh's find_dotnet_runtime_root against
# fake `dotnet` binaries so the split-SDK/runtime detection logic can be
# verified without depending on the actual toolchains installed on the
# machine running the test.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/pf-dotnet-runtime-test.XXXXXX")
trap 'rm -rf -- "$work"' EXIT

source "$repo_root/scripts/ci/dotnet-runtime.sh"

# fake_dotnet DIR RUNTIME_LINES... writes an executable `dotnet` stub under
# DIR/dotnet whose `--list-runtimes` output is RUNTIME_LINES (one per arg).
fake_dotnet() {
  local dir=$1
  shift
  mkdir -p "$dir"
  {
    printf '#!/usr/bin/env bash\n'
    printf 'if [ "$1" = "--list-runtimes" ]; then\n'
    for line in "$@"; do
      printf '  printf %s\\n\n' "$(printf '%q' "$line")"
    done
    printf 'fi\n'
  } >"$dir/dotnet"
  chmod +x "$dir/dotnet"
}

# 1) SDK-capable `dotnet` on PATH already carries the requested runtime -
#    the common case on CI machines where SDK and runtime share one root.
unified="$work/unified"
fake_dotnet "$unified" 'Microsoft.NETCore.App 8.0.29 [/x]'
root=$(PATH="$unified:$PATH" DOTNET_ROOT= find_dotnet_runtime_root 8)
test "$root" = "$unified"
printf '✅ unified SDK+runtime root is used directly\n'

# 1b) The same unified case, but the PATH entry is a symlink to the real
#     install (e.g. ~/.dotnet/dotnet -> /usr/share/dotnet/dotnet, exactly
#     how GitHub Actions' ubuntu-24.04 image and this devcontainer both
#     expose `dotnet`). dirname on the unresolved symlink path would give
#     a directory with no shared/Microsoft.NETCore.App tree of its own -
#     the root returned here must be the real install directory, not the
#     symlink's parent.
real_install="$work/real-install"
fake_dotnet "$real_install" 'Microsoft.NETCore.App 8.0.29 [/x]'
symlinked_path_entry="$work/symlinked-path-entry"
mkdir -p "$symlinked_path_entry"
ln -s "$real_install/dotnet" "$symlinked_path_entry/dotnet"
root=$(PATH="$symlinked_path_entry:$PATH" DOTNET_ROOT= find_dotnet_runtime_root 8)
test "$root" = "$real_install"
printf '✅ resolves a symlinked PATH dotnet to its real install root, not the symlink parent\n'

# 2) PATH `dotnet` has an SDK but not the requested runtime; a secondary,
#    runtime-only root does - the split-install case this script exists for.
sdk_only="$work/sdk-only"
runtime_only="$work/runtime-only"
fake_dotnet "$sdk_only" 'Microsoft.NETCore.App 9.0.14 [/x]' 'Microsoft.NETCore.App 10.0.4 [/x]'
fake_dotnet "$runtime_only" 'Microsoft.NETCore.App 8.0.29 [/x]'
root=$(PATH="$sdk_only:$PATH" DOTNET_ROOT="$runtime_only" find_dotnet_runtime_root 8)
test "$root" = "$runtime_only"
printf '✅ falls back to a detected secondary root when PATH dotnet lacks the runtime\n'

# 3) Nothing on PATH or in the fallback candidates carries the requested
#    runtime: the caller must get a clean failure, not a guessed path.
if PATH="$sdk_only:$PATH" DOTNET_ROOT="$runtime_only" find_dotnet_runtime_root 6 >"$work/out" 2>/dev/null; then
  echo "expected find_dotnet_runtime_root to fail when no root has the runtime" >&2
  exit 1
fi
test ! -s "$work/out"
printf '✅ fails cleanly (no output) when no root has the requested runtime\n'

# 4) An empty/unset DOTNET_ROOT candidate must not be probed as a path.
root=$(PATH="$sdk_only:$PATH" DOTNET_ROOT= find_dotnet_runtime_root 9)
test "$root" = "$sdk_only"
printf '✅ ignores an empty DOTNET_ROOT candidate instead of probing it\n'

printf '✅ dotnet runtime root detection behaves correctly for unified, split, and missing installs\n'

#!/usr/bin/env bash
# Get a fresh checkout ready to build and test entirely offline: verify the
# Go toolchain matches tools/go-version, then warm the module cache for the
# main module and every separate go.work module (plugins/*,
# cmd/platform-factory-installer) so no subsequent `go build`/`go test`
# needs network access. This is the "toolchain lock" half of the
# stabilization baseline (Sanetizer-todo.md phase 1, item 2) - distinct from
# scripts/local/bootstrap.sh, which builds installable binaries for END
# USERS, not a dev machine.
#
# Usage: scripts/bootstrap-dev.sh
set -euo pipefail

if [ -n "${BASH_VERSION:-}" ]; then
  script_path=${BASH_SOURCE[0]}
elif [ -n "${ZSH_VERSION:-}" ]; then
  script_path=${(%):-%x}
else
  echo "error: this script requires Bash or Zsh" >&2
  exit 1
fi
repo_root=$(cd "$(dirname "$script_path")/.." && pwd)
cd "$repo_root"

command -v go >/dev/null 2>&1 || {
  echo "error: Go is required on PATH - install the version in tools/go-version" >&2
  exit 1
}

required_version=$(tr -d '[:space:]' < tools/go-version)
installed_version=$(go version | sed -n 's/^go version go\([0-9.]*\).*/\1/p')
if [ "$installed_version" != "$required_version" ]; then
  echo "error: this repo is pinned to Go $required_version (tools/go-version), found $installed_version on PATH" >&2
  echo "  install it from https://go.dev/dl/ or via your version manager, then re-run this script" >&2
  exit 1
fi
echo "go toolchain: $installed_version (matches tools/go-version)" >&2

# Known, pre-existing gap (not introduced here): internal/hypervisor/sandbox's
# seccomp syscall table hardcodes x86_64 syscall numbers, so `go build ./...`
# fails specifically on linux/arm64 - unaffected on linux/amd64 and darwin/any.
# Surface this proactively instead of letting it appear as a confusing raw
# compiler error later.
if [ "$(go env GOOS)/$(go env GOARCH)" = "linux/arm64" ]; then
  echo "warning: linux/arm64 host detected - internal/hypervisor/sandbox does not build here yet" >&2
  echo "  (known gap: its seccomp syscall table is x86_64-only; see containers/dev/Dockerfile)" >&2
fi

# go.work's own `use` block is the single source of truth for which
# directories are separate modules - read it instead of hardcoding the list
# a second time here, so this script can't silently drift from go.work.
modules=$(awk '/^use \(/{flag=1; next} /^\)/{flag=0} flag {gsub(/^[ \t]+/, ""); if ($0 != ".") print}' go.work)

echo "downloading main module dependencies..." >&2
go mod download

for module in $modules; do
  echo "downloading $module dependencies..." >&2
  (cd "$module" && GOWORK=off go mod download)
done

echo "bootstrap complete: main module + $(printf '%s\n' "$modules" | wc -l | tr -d ' ') separate modules ready to build offline" >&2
echo "next: make verify" >&2

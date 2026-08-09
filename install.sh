#!/usr/bin/env bash
# Root-level convenience entry point: `./install.sh` from a clone of this
# repository. Delegates entirely to scripts/local/install.sh (the
# canonical, dependency-free installer - only Go and a POSIX shell are
# required); see that script for the full interactive/non-interactive
# option set and cmd/platform-factory-installer for the bubbletea-based
# equivalent.
set -euo pipefail

if [ -n "${BASH_VERSION:-}" ]; then
  script_path=${BASH_SOURCE[0]}
elif [ -n "${ZSH_VERSION:-}" ]; then
  script_path=${(%):-%x}
else
  echo "error: this script requires Bash or Zsh" >&2
  exit 1
fi
repo_root=$(cd "$(dirname "$script_path")" && pwd)

exec "$repo_root/scripts/local/install.sh" "$@"

#!/usr/bin/env bash
set -euo pipefail

engine="${1:-docker}"
case "$engine" in
  docker|podman) ;;
  *) echo "usage: $0 [docker|podman]" >&2; exit 2 ;;
esac

command -v "$engine" >/dev/null || {
  echo "Cannot start the PF demo: $engine is not installed." >&2
  exit 1
}
"$engine" info >/dev/null || {
  echo "Cannot start the PF demo: $engine is installed but not running." >&2
  exit 1
}

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
session_root="$(mktemp -d "${TMPDIR:-/tmp}/pf-user-demo.XXXXXX")"
project_name="pf-demo-$(basename "$session_root" | tr '[:upper:]' '[:lower:]')"
project_root="$session_root/$project_name"

mkdir -p "$session_root/bin" "$session_root/plugins" "$project_root"
cp "$repo_root/demo/hello-world/main.go" "$project_root/main.go"

echo "Preparing the real PF CLI and Go language plugin..."
(cd "$repo_root" && go build -o "$session_root/bin/pf" ./cmd/platform-factory)
(cd "$repo_root/plugins/lang-go" && go build -o "$session_root/plugins/platform-factory-lang-go" .)

export PATH="$session_root/bin:$PATH"
export PLATFORM_FACTORY_LANG_PLUGIN_DIR="$session_root/plugins"
export GOOS=linux GOARCH=amd64 CGO_ENABLED=0
export PF_DEMO_ENGINE="$engine"
export PF_DEMO_PROJECT="$project_root"

cd "$project_root"
printf '\n'
printf '╭─ Platform Factory · hands-on MVP ──────────────────────────\n'
printf '│ You are in a new repository containing only main.go.\n'
printf '│ Engine: %s\n' "$engine"
printf '│ Workspace: %s\n' "$project_root"
printf '╰────────────────────────────────────────────────────────────\n'
printf '\nType these commands yourself:\n\n'
printf '  pf init --dry-run --engine %s .   # review; writes nothing\n' "$engine"
printf '  pf init --engine %s .             # use the guided TUI\n' "$engine"
printf '  pf launch                         # build OCI and run it\n'
printf '  pf inspect .                      # inspect the retained layout\n'
printf '  pf plugin list                    # see the loaded language plugin\n'
printf '\nType exit when finished. The workspace is kept for inspection.\n\n'

if [[ -n "${SHELL:-}" ]] && [[ -x "$SHELL" ]]; then
  exec "$SHELL" -i
fi
exec /bin/sh -i

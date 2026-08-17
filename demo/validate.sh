#!/usr/bin/env bash
set -euo pipefail

engine="${1:-docker}"
case "$engine" in
  docker|podman) ;;
  *) echo "usage: $0 [docker|podman]" >&2; exit 2 ;;
esac

command -v "$engine" >/dev/null
"$engine" info >/dev/null

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
work_root="$(mktemp -d "${TMPDIR:-/tmp}/pf-mvp-demo.XXXXXX")"
project_name="pf-mvp-$(basename "$work_root" | tr '[:upper:]' '[:lower:]')"
project_root="$work_root/$project_name"
image_created=0
cleanup() {
  if [[ "$image_created" = "1" ]]; then
    "$engine" image rm "$project_name:latest" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$work_root"
}
trap cleanup EXIT

mkdir -p "$work_root/bin" "$work_root/plugins" "$project_root"
cp "$repo_root/demo/hello-world/main.go" "$project_root/main.go"

(cd "$repo_root" && go build -o "$work_root/bin/pf" ./cmd/platform-factory)
(cd "$repo_root/plugins/lang-go" && go build -o "$work_root/plugins/platform-factory-lang-go" .)

export PLATFORM_FACTORY_LANG_PLUGIN_DIR="$work_root/plugins"
export GOOS=linux GOARCH=amd64 CGO_ENABLED=0

cd "$project_root"
review="$($work_root/bin/pf init --dry-run --engine "$engine" .)"
test "$(find . -mindepth 1 -maxdepth 1 | wc -l | tr -d ' ')" = "1"
grep -q 'language go' <<<"$review"
grep -q 'pf.yaml' <<<"$review"
grep -q 'pf.lock' <<<"$review"

"$work_root/bin/pf" init --yes --engine "$engine" .
test -f pf.yaml
test -f pf.lock
# main.go (already present) plus pf.yaml, pf.lock, .gitignore, .git,
# and the .pf/policies/deploy/dist/reports scaffold directories pf init
# --yes creates.
test "$(find . -mindepth 1 -maxdepth 1 | wc -l | tr -d ' ')" = "10"

output="$($work_root/bin/pf launch)"
image_created=1
grep -q 'hello from pf mvp' <<<"$output"
test -f .platform-factory/image/index.json

printf '✅ PF MVP passed with %s: init → OCI → local image → hello world\n' "$engine"

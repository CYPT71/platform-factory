#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
work_root="$(mktemp -d "${TMPDIR:-/tmp}/pf-personas.XXXXXX")"
cleanup() {
  chmod -R u+w "$work_root" 2>/dev/null || true
  rm -rf -- "$work_root"
}
trap cleanup EXIT
export GOCACHE="$work_root/go-build-cache"

go build -o "$work_root/pf" ./cmd/platform-factory
go build -o "$work_root/conformance" ./cmd/platform-factory-conformance
mkdir -p "$work_root/plugins"
(cd "$repo_root/plugins/lang-go" && go build -o "$work_root/plugins/platform-factory-lang-go" .)
export PLATFORM_FACTORY_LANG_PLUGIN_DIR="$work_root/plugins"

# Junior: begin with one source file, review without writes, initialize, then
# produce the complete deterministic build outputs without knowing OCI paths.
mkdir -p "$work_root/junior"
cp "$repo_root/demo/hello-world/main.go" "$work_root/junior/main.go"
(
  cd "$work_root/junior"
  before="$(find . -mindepth 1 -maxdepth 1 | sort)"
  "$work_root/pf" init --dry-run --engine docker . >/dev/null
  test "$(find . -mindepth 1 -maxdepth 1 | sort)" = "$before"
  "$work_root/pf" init --yes --engine docker . >/dev/null
  test -f pf.yaml
  test -f pf.lock
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o hello main.go
  "$work_root/pf" build --dist dist --reports reports \
    --entrypoint /app/hello --format text hello
  "$work_root/pf" verify dist/oci-layout >/dev/null
  test -f dist/sbom.json
  test -f reports/build.json
)

mkdir -p "$work_root/intermediate"
cp -R "$repo_root/examples/sdk/plugin-python-lazy-docker" "$work_root/intermediate/plugin"
cp -R "$repo_root/sdk/plugin-python" "$work_root/intermediate/sdk"
sed -i.bak 's#"..", "..", "..", "sdk", "plugin-python"#"..", "sdk"#' "$work_root/intermediate/plugin/plugin.py"
rm "$work_root/intermediate/plugin/plugin.py.bak"
"$work_root/conformance" plugin "$work_root/intermediate/plugin/plugin.py"
PYTHONPATH="$work_root/intermediate/sdk" python3 -m unittest discover -s "$work_root/intermediate/plugin" -v
mkdir -p "$work_root/intermediate/app"
cp "$repo_root/demo/hello-world/main.go" "$work_root/intermediate/app/main.go"
(
  cd "$work_root/intermediate/app"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o hello main.go
  "$work_root/pf" build --output "$work_root/intermediate/oci" \
    --entrypoint /app/hello --format text hello
)
test -f "$work_root/intermediate/oci/index.json"

mkdir -p "$work_root/senior/examples/hello-pipeline"
cp -R "$repo_root/examples/hello-pipeline/app" "$work_root/senior/examples/hello-pipeline/app"
cp "$repo_root/examples/pipeline.json" "$work_root/senior/pipeline.json"
(
  cd "$work_root/senior"
  "$work_root/pf" pipeline plan --format text pipeline.json
  PLATFORM_FACTORY_TRACE_ID=senior-empty-repo "$work_root/pf" pipeline run \
    --sandbox auto --parallelism 2 --workdir "$work_root/senior/run" \
    --format text pipeline.json || {
      sed -n '1,240p' "$work_root/senior/run/journal.json"
      exit 1
    }
)
test -f "$work_root/senior/run/journal.json"
test -x "$work_root/senior/run/out/dist/hello-pipeline"
"$work_root/senior/run/out/dist/hello-pipeline" | grep -q 'hello, world'

printf '✅ Junior build, intermediate SDK/OCI, and senior pipeline experiences passed from clean workspaces\n'

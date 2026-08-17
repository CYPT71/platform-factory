#!/usr/bin/env bash
#
# Local reproduction of .github/workflows/ci-quality.yml's amd64 lane.
# (Kept under scripts/ci/verify/ rather than the repo root, since root-level
# scratch files in this environment have repeatedly been swept away by
# something outside this script's control.)
set -euo pipefail

ROOT="${GITHUB_WORKSPACE:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)}"
cd "$ROOT"
export GITHUB_WORKSPACE="$ROOT" GOTOOLCHAIN=local

echo "--- Record hermetic toolchain identity ---"
go version
go env GOROOT GOVERSION GOOS GOARCH GOTOOLCHAIN
test "$(go env GOTOOLCHAIN)" = local

echo "--- Format, vet, and test ---"
test -z "$(find api cmd conformance examples internal sdk tests -type f -name '*.go' -print0 | xargs -0 gofmt -l)"
go vet ./...
go test ./...
(cd cmd/platform-factory-installer && GOWORK=off go vet ./... && GOWORK=off go test ./...)

echo "--- Execute a pipeline end to end through the shipped CLI ---"
work=$(mktemp -d "${TMPDIR:-/tmp}/pf-verify-quality.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
go build -o "$work/platform-factory" ./cmd/platform-factory
cat > "$work/pipeline.json" <<'JSON'
{
  "api_version": "platform-factory.dev/v1alpha1",
  "name": "ci-pipeline",
  "required_capabilities": ["cache", "parallel-stages"],
  "stages": [
    {"id": "resolve", "command": {"executable": "/bin/true"}},
    {"id": "compile", "depends_on": ["resolve"], "command": {"executable": "/bin/sh", "args": ["-c", "printf built > out.bin"]}, "outputs": [{"name": "bin", "path": "/out.bin"}]},
    {"id": "test", "depends_on": ["resolve"], "command": {"executable": "/bin/true"}},
    {"id": "package", "depends_on": ["compile", "test"], "command": {"executable": "/bin/true"}, "inputs": [{"stage": "compile", "name": "bin", "target": "/in/bin"}]}
  ]
}
JSON
"$work/platform-factory" pipeline plan "$work/pipeline.json"
"$work/platform-factory" pipeline run --sandbox off --workdir "$work/run" "$work/pipeline.json"
python3 -c "import json; j=json.load(open('$work/run/journal.json')); assert j['api_version']=='platform-factory.dev/journal/v1', j; assert all(s['state']=='succeeded' for s in j['stages']), j"
"$work/platform-factory" pipeline run --sandbox off --workdir "$work/run" "$work/pipeline.json"
python3 -c "import json; j=json.load(open('$work/run/journal.json')); assert any(s.get('cache')=='hit' for s in j['stages']), j"

echo "--- Execute every portable user example from its public entrypoint ---"
examples/run-all.sh

echo "--- Run the public conformance suite from outside the source tree ---"
go build -o "$work/platform-factory-conformance" ./cmd/platform-factory-conformance
go build -o "$work/platform-factory-plugin-demo" ./cmd/platform-factory-plugin-demo
(cd "$work" && ./platform-factory-conformance vectors)
(cd "$work" && ./platform-factory-conformance plugin "$work/platform-factory-plugin-demo")
./examples/sdk/plugin-python/plugin.py </dev/null
./examples/sdk/plugin-javascript/plugin.js </dev/null
"$work/platform-factory-conformance" plugin ./examples/sdk/plugin-python/plugin.py
"$work/platform-factory-conformance" plugin ./examples/sdk/plugin-javascript/plugin.js
(cd examples/sdk/plugin-typescript && npm install --no-audit --no-fund && npm run build)
"$work/platform-factory-conformance" plugin ./examples/sdk/plugin-typescript/plugin.js
dotnet publish examples/sdk/plugin-csharp/SecureOciPlugin.csproj \
  --configuration Release --output "$work/platform-factory-plugin-csharp"
source scripts/ci/dotnet-runtime.sh
csharp_runtime_root=$(find_dotnet_runtime_root 8) || {
  echo "C# conformance requires Microsoft.NETCore.App 8.x" >&2
  exit 1
}
DOTNET_ROOT="$csharp_runtime_root" "$work/platform-factory-conformance" plugin \
  "$work/platform-factory-plugin-csharp/SecureOciPlugin"
(cd "$work" && ./platform-factory-conformance backend)
(cd "$work" && ./platform-factory-conformance publication)

echo "--- Run explicit CLI, OCI, and mTLS regression tests ---"
go test ./cmd/oci-builder -run 'TestRunWithoutArgumentsShowsUsage|TestRunArgumentErrors|TestRunBuildsLayout'
go test ./internal/oci -run 'TestBuildWritesValidLayout|TestBuildRejectsUnsafeInputs|TestBuildErrors|TestNormalizeValidation|TestWriteLayoutErrors'
go test ./internal/mtls -count=1

echo "--- Build local Linux and Windows command environments ---"
bash -n scripts/local/bootstrap.sh
bootstrap_root=$(mktemp -d "${TMPDIR:-/tmp}/pf-verify-bootstrap.XXXXXX")
scripts/local/bootstrap.sh --target linux/amd64 --env "$bootstrap_root/linux"
scripts/local/bootstrap.sh --target windows/amd64 --env "$bootstrap_root/windows"
test -x "$bootstrap_root/linux/bin/platform-factory"
test -x "$bootstrap_root/linux/bin/oci-builder"
test -x "$bootstrap_root/linux/bin/example-service"
test -x "$bootstrap_root/linux/bin/microvm-init"
test -f "$bootstrap_root/windows/bin/platform-factory.exe"
test -f "$bootstrap_root/windows/bin/oci-builder.exe"
test -f "$bootstrap_root/windows/bin/example-service.exe"
test -f "$bootstrap_root/windows/bin/microvm-init.exe"

echo "--- Prove containerd node installation is idempotent and reversible ---"
scripts/microvm/test-install-containerd-runtime.sh
bash -c 'source "$1/activate"; command -v platform-factory; deactivate_platform_factory' \
  bash "$bootstrap_root/linux"
pwsh -NoProfile -Command \
  '$tokens=$null; $errors=$null; [System.Management.Automation.Language.Parser]::ParseFile("scripts/local/bootstrap.ps1",[ref]$tokens,[ref]$errors) | Out-Null; if ($errors.Count) { exit 1 }'

echo "--- Smoke-test installers ---"
bash -n scripts/local/install.sh
bash_prefix=$(mktemp -d "${TMPDIR:-/tmp}/pf-verify-install.XXXXXX")
scripts/local/install.sh --components builder --prefix "$bash_prefix" --yes
test -x "$bash_prefix/platform-factory"
test -x "$bash_prefix/oci-builder"
test ! -e "$bash_prefix/microvm-init"
"$bash_prefix/platform-factory" version
go_prefix=$(mktemp -d "${TMPDIR:-/tmp}/pf-verify-install-go.XXXXXX")
go run ./cmd/platform-factory-installer -components microvm -prefix "$go_prefix" -yes
test -x "$go_prefix/platform-factory"
test -x "$go_prefix/microvm-init"
test -x "$go_prefix/microvm-initramfs"
test ! -e "$go_prefix/oci-builder"
"$go_prefix/platform-factory" version
go run ./cmd/platform-factory-installer -list
! scripts/local/install.sh --components does-not-exist --yes --prefix "$bash_prefix" 2>/dev/null

echo "--- Cross-compile and verify reproducible OCI builds (amd64) ---"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=true -o "$work/oci-builder-amd64" ./cmd/oci-builder
scripts/ci/verify-elf.sh "$work/oci-builder-amd64" amd64
"$work/oci-builder-amd64" -binary "$work/oci-builder-amd64" -output "$work/oci-image" -arch amd64
python3 scripts/ci/verify-oci-layout.py "$work/oci-image"

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=true -o "$work/oci-builder-repro" ./cmd/oci-builder
"$work/oci-builder-repro" -binary "$work/oci-builder-repro" -output "$work/oci-image-repro" -arch amd64 -created 1970-01-01T00:00:00Z
python3 scripts/ci/verify-oci-layout.py "$work/oci-image-repro"
python3 scripts/ci/negative-oci-layout.py scripts/ci/verify-oci-layout.py "$work/oci-image-repro"

echo "ci-quality: PASS (race+coverage gate not reproduced here - already covered by scripts/ci/verify/security-analysis.sh's 'go test -race ./...')"

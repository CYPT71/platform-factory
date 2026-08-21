#!/usr/bin/env bash
set -euo pipefail
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH= cd -- "$here/../.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/platform-factory-sdk.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
cd "$repo"
go run ./examples/sdk/pipeline ./examples/pipeline.json
go run ./examples/sdk/oci
go run ./examples/sdk/microvm
go build -trimpath -o "$work/conformance" ./cmd/platform-factory-conformance
go build -trimpath -o "$work/plugin-go" ./examples/sdk/plugin-go
"$work/conformance" plugin "$work/plugin-go"
"$work/conformance" plugin "$here/plugin-python/plugin.py"
"$work/conformance" plugin "$here/plugin-javascript/plugin.js"
echo "Go, Python and JavaScript plugins conform to the same stable protocol"

if command -v npm >/dev/null 2>&1; then
  (cd "$here/plugin-typescript" && npm install --no-audit --no-fund --silent && npm run build --silent)
  "$work/conformance" plugin "$here/plugin-typescript/plugin.js"
  echo "TypeScript plugin conforms to the same stable protocol"
else
  echo "TypeScript plugin skipped: npm is not on PATH"
fi

# dotnet without the matching net8.0 runtime publishes fine but the
# published apphost fails at process launch (never reaches the RPC
# handshake), which the conformance suite would otherwise report as a
# confusing "hello call: EOF" instead of a clear skip.
if command -v dotnet >/dev/null 2>&1 && dotnet --list-runtimes 2>/dev/null | grep -q '^Microsoft\.NETCore\.App 8\.'; then
  dotnet publish "$here/plugin-csharp/SecureOciPlugin.csproj" \
    --configuration Release --output "$work/plugin-csharp" --nologo --verbosity quiet
  "$work/conformance" plugin "$work/plugin-csharp/SecureOciPlugin"
  echo "C# plugin conforms to the same stable protocol"
else
  echo "C# plugin skipped: dotnet with the net8.0 runtime is not available"
fi

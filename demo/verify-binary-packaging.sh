#!/usr/bin/env bash
# Local, engine-free proof: deterministic packaging and tamper rejection.
set -euo pipefail

command -v go >/dev/null || { echo 'Install the Go version pinned in go.mod.' >&2; exit 1; }
command -v python3 >/dev/null || { echo 'Python 3 is required to compare the OCI descriptors.' >&2; exit 1; }
repo_root="$(cd "$(dirname "$0")/.." && pwd)"
demo_root="$(mktemp -d "${TMPDIR:-/tmp}/pf-binary-proof.XXXXXX")"
mkdir -p "$demo_root/bin"

printf 'Building local tools from this checkout; outputs: %s\n' "$demo_root"
(cd "$repo_root" && CGO_ENABLED=0 GOWORK=off go build -trimpath -o "$demo_root/bin/pf" ./cmd/platform-factory)
(cd "$repo_root" && CGO_ENABLED=0 GOWORK=off go build -trimpath -o "$demo_root/bin/oci-builder" ./cmd/oci-builder)
(cd "$repo_root" && CGO_ENABLED=0 GOWORK=off GOOS=linux GOARCH=amd64 go build -trimpath -o "$demo_root/service" ./cmd/example-service)

printf '\n1. Package the same prebuilt Linux executable twice.\n'
"$demo_root/bin/oci-builder" -binary "$demo_root/service" -output "$demo_root/image-a" -arch amd64
"$demo_root/bin/oci-builder" -binary "$demo_root/service" -output "$demo_root/image-b" -arch amd64
"$demo_root/bin/pf" verify "$demo_root/image-a"
"$demo_root/bin/pf" verify "$demo_root/image-b"

python3 - "$demo_root" <<'PY'
import json
from pathlib import Path
import shutil
import sys
root = Path(sys.argv[1])
first = json.loads((root / 'image-a/index.json').read_text())['manifests'][0]['digest']
second = json.loads((root / 'image-b/index.json').read_text())['manifests'][0]['digest']
if first != second:
    raise SystemExit(f'Determinism check failed: {first} != {second}')
print(f'Same OCI manifest digest: {first}')
shutil.copytree(root / 'image-a', root / 'image-tampered')
PY

printf '\n2. Verify the untouched copy before changing it.\n'
"$demo_root/bin/pf" verify "$demo_root/image-tampered"
python3 - "$demo_root/image-tampered" <<'PY'
import json
from pathlib import Path
import sys
root = Path(sys.argv[1])
digest = json.loads((root / 'index.json').read_text())['manifests'][0]['digest']
blob = root / 'blobs/sha256' / digest.split(':', 1)[1]
# Appending whitespace preserves JSON validity but changes its size/digest.
blob.write_bytes(blob.read_bytes() + b'\n')
PY

printf '\n3. Require verification to reject the modified artifact.\n'
if "$demo_root/bin/pf" verify "$demo_root/image-tampered"; then
  echo 'FAIL: verification accepted the modified artifact.' >&2
  exit 1
fi
printf '\nPASS: same input, identical manifest digest; modified artifact refused.\n'
printf 'This proves this packaging fixture, not universal source-build reproducibility.\n'
printf 'No container was run, no registry contacted, and nothing published.\n'
printf 'Inspect retained outputs at: %s\n' "$demo_root"

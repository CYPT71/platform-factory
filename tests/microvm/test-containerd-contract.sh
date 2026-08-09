#!/usr/bin/env bash
# Exercise the containerd config generator and the OCI CLI's create/state/
# delete contract without starting the supervisor's KVM_RUN path. No daemon,
# root, network, or KVM is required. The CLI's own create/state/delete
# handling is exercised through the flag shape containerd's runc-v2 shim
# emits, since platform-factory-runtime still accepts it for compatibility
# even though the shipped shim is now platform-factory-shim (see
# plugins/containerd/cmd/platform-factory-shim).
set -euo pipefail

if [ "$(uname -s)" != Linux ]; then
  echo "SKIP: containerd runtime contract executes on Linux; validating cross-build"
  output=${TMPDIR:-/tmp}/secure-oci-runtime-contract.test
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c \
    -o "$output" ./cmd/platform-factory-runtime
  rm -f "$output"
  exit 0
fi

go test ./plugins/containerd/internal/containerdshim ./plugins/containerd/cmd/platform-factory-containerd
go test ./cmd/platform-factory-runtime \
  -run '^(TestContainerdRuncV2PreStartContract|TestRunAcceptsContainerdKillAllShape)$' \
  -count=1 -v

temporary=$(mktemp -d "${TMPDIR:-/tmp}/secure-oci-containerd.XXXXXX")
trap 'rm -rf "$temporary"' EXIT
go build -trimpath -o "$temporary/platform-factory-containerd" ./plugins/containerd/cmd/platform-factory-containerd
"$temporary/platform-factory-containerd" config >"$temporary/90-secure-oci-runtime.toml"

if command -v containerd >/dev/null 2>&1; then
  {
    echo 'version = 2'
    printf 'imports = ["%s"]\n' "$temporary/90-secure-oci-runtime.toml"
  } >"$temporary/config.toml"
  containerd --config "$temporary/config.toml" config dump >"$temporary/dump.toml"
  grep -F 'io.containerd.platform-factory.v1' "$temporary/dump.toml"
  grep -F 'sandboxer' "$temporary/dump.toml" | grep -F 'shim'
else
  echo "SKIP: containerd binary unavailable; renderer and lifecycle contracts passed"
fi

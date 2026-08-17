#!/usr/bin/env bash
# Build a deterministic OCI layout for the platform-factory MCP server
# (`pf mcp serve`), using this repository's own native OCI builder
# (cmd/oci-builder) - no Dockerfile RUN steps ever produce image content
# here; Docker only re-wraps the already-built, already-verified layout
# for registry push (see the repository-root Dockerfile).
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: build-mcp-image-layout.sh OUTPUT BINARY ARCH" >&2
  exit 2
fi

output=$1
binary=$2
arch=$3

host_builder=$(mktemp "${RUNNER_TEMP:-/tmp}/oci-builder-host.XXXXXX")
config_file=$(mktemp "${RUNNER_TEMP:-/tmp}/mcp-image-config.XXXXXX.json")
trap 'rm -f "$host_builder" "$config_file"' EXIT

CGO_ENABLED=0 go build -trimpath -buildvcs=true -o "$host_builder" ./cmd/oci-builder
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -buildvcs=true -o "$binary" ./cmd/platform-factory
scripts/ci/verify-elf.sh "$binary" "$arch"

# Only "entrypoint" and any extra/system files actually survive into the
# published image: the repository-root Dockerfile's unpack stage reads
# .config.Entrypoint (via the /entrypoint symlink trick, since a FROM
# scratch image has no shell to compute it at container-start time) and
# copies the layer's file content verbatim, but it never reads
# .config.Cmd or .config.WorkingDir - verified by hand: baking "args"
# into this config and building the real image still launched with no
# arguments, printing platform-factory's top-level help instead of
# starting the MCP server. So the full `mcp serve --repo /workspace`
# invocation is the MCP client's job (see docs/mcp.md's docker config
# example), the same way any other MCP server run via `docker run ...
# IMAGE <args>` works - not something this image bakes in as a default.
#
# system_files.ca_certificates is a HOST path this process reads the CA
# bundle from - it always lands at /etc/ssl/certs/ca-certificates.crt
# *inside* the image regardless of where it came from on the host (see
# cmd/oci-builder/main.go's fixed destination list). CI's Linux runners
# have that same path natively; a macOS build host doesn't, so this picks
# the actual system root store there instead (/etc/ssl/cert.pem, present
# by default, no sudo/system changes needed) rather than requiring one to
# be created at the Linux path first.
ca_certificates=/etc/ssl/certs/ca-certificates.crt
if [ ! -r "$ca_certificates" ] && [ -r /etc/ssl/cert.pem ]; then
  ca_certificates=/etc/ssl/cert.pem
fi
cat >"$config_file" <<JSON
{
  "entrypoint": "/app/platform-factory",
  "system_files": {"ca_certificates": "$ca_certificates"}
}
JSON

"$host_builder" -binary "$binary" -output "$output" -arch "$arch" -created 1970-01-01T00:00:00Z \
  -config "$config_file" -image platform-factory-mcp -tag latest
python3 scripts/ci/verify-oci-layout.py "$output"

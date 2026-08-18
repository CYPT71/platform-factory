#!/usr/bin/env bash
set -euo pipefail
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH= cd -- "$here/../.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/secure-oci-example-project.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
cd "$repo"
go build -trimpath -o "$work/platform-factory" ./cmd/platform-factory
cp -R "$here" "$work/project"
"$work/platform-factory" project show --config "$work/project/.config_image.yaml" "$work/project"
python=$(command -v python3 || command -v python)
mkdir -p "$work/bin"
ln -s "$python" "$work/bin/python"
PATH="$work/bin:$PATH" "$work/platform-factory" freeze --config "$work/project/.config_image.yaml" "$work/project"

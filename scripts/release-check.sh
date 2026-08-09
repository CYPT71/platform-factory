#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

echo "[release-check] verifying pinned Go toolchain"
make bootstrap

echo "[release-check] running repository verification"
make verify

echo "[release-check] running CLI diagnostics"
go run ./cmd/platform-factory doctor --json >/tmp/platform-factory-doctor.json

echo "[release-check] release checks passed"

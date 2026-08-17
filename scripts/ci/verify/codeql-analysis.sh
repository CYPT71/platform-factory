#!/usr/bin/env bash
# .github/workflows/ci-codeql.yml's only project-owned step is "Build Go
# for CodeQL extraction" (go build ./...); the actual static-analysis scan
# is entirely the github/codeql-action third-party action plus the hosted
# CodeQL CLI/database, which this script does not attempt to reproduce -
# it requires a multi-hundred-MB CLI download and GitHub-side query packs
# that have no meaningful local equivalent.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
export GOTOOLCHAIN=local

echo "--- Build Go for CodeQL extraction ---"
go build ./...

echo "NOTE: the CodeQL scan itself (github/codeql-action/analyze) was not reproduced locally - it needs the hosted CodeQL CLI and query packs"
echo "codeql-analysis: PASS (build-only; scan not locally reproducible)"

#!/usr/bin/env bash
# Local reproduction of .github/workflows/ci-reproducibility.yml's Linux
# lane: portable-rebuild's deterministic-build tests (the macOS-only
# native-VMM step is skipped, matching the workflow's own runner.os
# guard), then rebuild-a/rebuild-b/compare for amd64 and arm64.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
export GOTOOLCHAIN=local SOURCE_DATE_EPOCH=0 TZ=UTC LANG=C.UTF-8 LC_ALL=C.UTF-8

echo "--- Verify deterministic rebuilds on the host OS ---"
go test ./internal/oci ./internal/runtime ./internal/hypervisor/... ./cmd/platform-factory \
  -run "Test(BuildIsDeterministic|BuildSemanticLayersIsDeterministic|RunBuildRebuildVerifiesReproducibility|ProjectDoubleBuildProducesIdenticalDigest|ProbeNativeIsActionable)" \
  -count=1
echo "NOTE: the macOS-only 'native VMM' step was skipped (this is not macOS)"

work=$(mktemp -d "${TMPDIR:-/tmp}/pf-verify-repro.XXXXXX")
trap 'rm -rf -- "$work"' EXIT

for arch in amd64 arm64; do
  echo "--- Build independently reproducible layout A/B ($arch) ---"
  # rebuild-a and rebuild-b each produce a plainly named "layout"/"oci-builder"
  # in the original workflow (separate jobs, compared afterward as separate
  # artifacts); build each variant in its own subdirectory here so the tar
  # member names match between A and B - naming them differently would make
  # the archives differ by construction, not because the rebuild itself is
  # non-deterministic.
  mkdir -p "$work/a-$arch" "$work/b-$arch"
  scripts/ci/build-verified-layout.sh "$work/a-$arch/layout" "$work/a-$arch/oci-builder" "$arch"
  tar --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner \
    -cf "$work/layout-a-$arch.tar" -C "$work/a-$arch" layout oci-builder

  scripts/ci/build-verified-layout.sh "$work/b-$arch/layout" "$work/b-$arch/oci-builder" "$arch"
  tar --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner \
    -cf "$work/layout-b-$arch.tar" -C "$work/b-$arch" layout oci-builder

  echo "--- Compare independent rebuild bytes ($arch) ---"
  cmp "$work/layout-a-$arch.tar" "$work/layout-b-$arch.tar"
  echo "reproducibility[$arch]: byte-identical independent rebuilds confirmed"
done

echo "reproducibility: PASS"

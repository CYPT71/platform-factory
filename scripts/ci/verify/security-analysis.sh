#!/usr/bin/env bash
# Local reproduction of .github/workflows/ci-security.yml's static-analysis
# job (govulncheck/license/allowlist/regression checks), plus the
# non-PR-specific parts of pr-policy. The PR-specific base-SHA diff-check
# is skipped outside an actual pull request, since there is no PR base to
# compare against locally.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
export GOTOOLCHAIN=local

echo "--- Validate workflow supply-chain controls ---"
python3 scripts/ci/verify-workflows.py

echo "--- Static analysis and security regression tests ---"
go vet ./...
work=$(mktemp -d "${TMPDIR:-/tmp}/pf-verify-security.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 -json ./... > "$work/govulncheck.json"
{ go list -deps -test -json ./...
  (cd cmd/platform-factory-installer && GOWORK=off go list -deps -test -json ./...)
  (cd plugins/containerd && GOWORK=off go list -deps -test -json ./...)
  (cd plugins/kubevirt && GOWORK=off go list -deps -test -json ./...)
} | python3 scripts/ci/check-license-policy.py --packages \
    --policy .github/license-policy.json --output "$work/license-report.json"
go test -race ./...

test -z "$(find cmd internal -name '*.go' -type f \
    ! -path 'cmd/microvm-init/main.go' \
    ! -path 'cmd/microvm-init/guest_agent.go' \
    ! -path 'cmd/microvm-init/identity_linux.go' \
    ! -path 'cmd/microvm-init/identity_other.go' \
    ! -path 'cmd/platform-factory/main.go' \
    ! -path 'cmd/platform-factory/exitcriteria_test.go' \
    ! -path 'cmd/platform-factory/kubevirt_plugin_test.go' \
    ! -path 'cmd/platform-factory/microvm_native_darwin.go' \
    ! -path 'cmd/platform-factory/microvm_native_kvm_test.go' \
    ! -path 'cmd/platform-factory/microvm_native_linux_amd64.go' \
    ! -path 'cmd/platform-factory/microvm_native_shared.go' \
    ! -path 'cmd/platform-factory/plugin.go' \
    ! -path 'cmd/platform-factory/provisionruntime.go' \
    ! -path 'cmd/platform-factory/plugin_provenance_test.go' \
    ! -path 'cmd/platform-factory/plugin_test.go' \
    ! -path 'cmd/platform-factory-installer/main.go' \
    ! -path 'internal/app/doctor/doctor.go' \
    ! -path 'internal/archtest/archtest.go' \
    ! -path 'internal/budget/stage_usage_test.go' \
    ! -path 'internal/executor/executor.go' \
    ! -path 'internal/executor/rlimit_linux.go' \
    ! -path 'internal/executor/rlimit_linux_test.go' \
    ! -path 'internal/executor/rlimit_other.go' \
    ! -path 'internal/executor/sandbox_linux.go' \
    ! -path 'internal/executor/sandbox_linux_test.go' \
    ! -path 'internal/executor/sandbox_other.go' \
    ! -path 'internal/executor/sandbox_other_test.go' \
    ! -path 'internal/executor/sandboxed.go' \
    ! -path 'internal/executor/cgroup_linux.go' \
    ! -path 'internal/hypervisor/sandbox/sandbox_linux_test.go' \
    ! -path 'internal/hypervisor/sandbox/seccomp_linux_test.go' \
    ! -path 'internal/hypervisor/sandbox/syscalls_linux.go' \
    ! -path 'conformance/conformance.go' \
    ! -path 'conformance/conformance_test.go' \
    ! -path 'internal/plugin/client.go' \
    ! -path 'internal/plugin/migration_thirdparty_test.go' \
    ! -path 'internal/plugin/subprocess_test.go' \
    ! -path 'internal/plugin/thirdparty_test.go' \
    ! -path 'internal/plugin/sandbox_linux.go' \
    ! -path 'internal/plugin/sandbox_other.go' \
    ! -path 'internal/plugin/sandbox_linux_test.go' \
    ! -path 'internal/provenance/plugin.go' \
    ! -path 'internal/provenance/plugin_test.go' \
    ! -path 'internal/sbom/sbom_test.go' \
    ! -path 'internal/ociruntime/supervisor_linux.go' \
    ! -path 'internal/ociruntime/apparmor_linux_test.go' \
    ! -path 'internal/ociruntime/runtime_linux_test.go' \
    ! -path 'internal/app/projectinit/projectinit.go' \
    ! -path 'internal/app/projectinit/projectinit_test.go' \
    ! -path 'internal/marketplace/sync.go' \
    ! -path 'internal/marketplace/gitfixture_test.go' \
    ! -path 'internal/mcp/git/exec.go' \
    ! -path 'internal/mcp/git/gitfixture_test.go' \
    ! -path 'internal/mcp/project/inspect_test.go' \
    ! -path 'internal/mcp/core/validate.go' \
    ! -path 'internal/mcp/core/affected.go' \
    ! -path 'internal/mcp/core/affected_test.go' \
    ! -path 'internal/mcp/plugins/validate.go' \
    ! -path 'internal/mcp/agent/orchestrate_test.go' \
    ! -path 'internal/mcp/agent/implement_test.go' \
    ! -path 'internal/mcp/product/product.go' \
    ! -path 'internal/mcp/product/product_test.go' \
    ! -path 'cmd/tui/marketplacetui/tui_test.go' \
    ! -path 'cmd/platform-factory/init_test.go' \
    ! -path 'cmd/platform-factory/init_e2e_test.go' \
    ! -path 'cmd/platform-factory/marketplace_e2e_test.go' \
    ! -path 'cmd/platform-factory/main_test.go' \
    -exec grep -nE 'os/exec|exec\.Command' {} + || true)"
test -z "$(find cmd internal -name '*.go' -type f \
  -exec grep -n 'InsecureSkipVerify' {} + || true)"
test -z "$(grep -rniI 'skopeo' cmd internal --include='*.go' || true)"
test -z "$(grep -rniI 'cosign' cmd internal --include='*.go' || true)"

echo "--- Enforce repository and workflow policy (non-PR-specific parts) ---"
test -z "$(git ls-files | grep -E '(^|/)(coverage\.out|coverage\.txt|oci-image|oci-builder|service)$' || true)"
test -z "$(grep -RInE --exclude=README.md --exclude-dir=.git --exclude-dir=.github 'TODO|FIXME|placeholder' . || true)"
echo "NOTE: pr-policy's base-SHA diff-check step was skipped (no PR base SHA outside an actual pull request)"

echo "security-analysis: PASS"

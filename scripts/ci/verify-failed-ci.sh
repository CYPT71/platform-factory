#!/usr/bin/env bash
# Locally reproduces every workflow that failed on origin/main, one at a
# time, each isolated in its own cgroup v2 group (scripts/ci/run-in-cgroup.sh)
# so a runaway step in one workflow can't starve or get blamed on another.
# This does NOT use the project's own --sandbox=require pipeline executor:
# that pivots each stage into a private mount/network/user namespace,
# which would also require re-mounting the docker/podman socket, /dev/kvm,
# and the module cache into every stage by hand for these checks to still
# work. Plain cgroup isolation gives the requested containment without that.
#
# Usage: scripts/ci/verify-failed-ci.sh [workflow...]
#   With no arguments, runs every workflow below in order.
#   PF_VERIFY_KIND_FULL=1     also attempt the real kind cluster suite
#   PF_VERIFY_MICROVM_FULL=1  also attempt the full kernel build+boot suite
set -euo pipefail
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$repo_root"

run_in_cgroup="$repo_root/scripts/ci/run-in-cgroup.sh"
log_dir=$(mktemp -d "${TMPDIR:-/tmp}/pf-verify-failed-ci.XXXXXX")
echo "logs: $log_dir"

# name -> script, in the order they'll run.
workflows_name=(ci-quality security-analysis pf-init-experience reproducibility codeql-analysis kind-multinode-runtime microvm-boot)
workflows_script=(
  "$repo_root/scripts/ci/verify/ci-quality.sh"
  "$repo_root/scripts/ci/verify/security-analysis.sh"
  "$repo_root/scripts/ci/verify/pf-init-experience.sh"
  "$repo_root/scripts/ci/verify/reproducibility.sh"
  "$repo_root/scripts/ci/verify/codeql-analysis.sh"
  "$repo_root/scripts/ci/verify/kind-multinode.sh"
  "$repo_root/scripts/ci/verify/microvm-boot.sh"
)

requested=("$@")
results_name=()
results_status=()
results_duration=()

for i in "${!workflows_name[@]}"; do
  name="${workflows_name[$i]}"
  if [ "${#requested[@]}" -gt 0 ]; then
    found=0
    for r in "${requested[@]}"; do [ "$r" = "$name" ] && found=1; done
    [ "$found" -eq 1 ] || continue
  fi

  script="${workflows_script[$i]}"
  log="$log_dir/$name.log"
  echo ""
  echo "=== $name ==="
  start=$(date +%s)
  # shellcheck disable=SC2086
  if "$run_in_cgroup" "$name" 4096 2000 0 -- bash -c "$script" >"$log" 2>&1; then
    status=PASS
    grep -q ': SKIP' "$log" && status="PASS (partial skip - see log)"
  else
    status=FAIL
  fi
  end=$(date +%s)
  tail -5 "$log" | sed 's/^/    /'
  echo "--- $name: $status (${log})"
  results_name+=("$name")
  results_status+=("$status")
  results_duration+=("$(( end - start ))")
done

echo ""
echo "================ summary ================"
fail=0
for i in "${!results_name[@]}"; do
  printf '%-24s %-6s %5ss\n' "${results_name[$i]}" "${results_status[$i]}" "${results_duration[$i]}"
  [ "${results_status[$i]}" = FAIL ] && fail=1
done
echo "logs: $log_dir"
exit "$fail"

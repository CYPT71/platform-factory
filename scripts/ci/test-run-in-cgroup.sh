#!/usr/bin/env bash
# Exercises scripts/ci/run-in-cgroup.sh: exit status passthrough, pids.max
# enforcement, cpu.max/memory.max content, and that the created cgroup is
# always removed afterward - success or failure.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
runner="$repo_root/scripts/ci/run-in-cgroup.sh"

if [ ! -w /sys/fs/cgroup ]; then
  echo "SKIP: /sys/fs/cgroup is not writable (no cgroup v2 delegation here)"
  exit 0
fi

no_leftovers() {
  ! ls /sys/fs/cgroup 2>/dev/null | grep -q '^pf-verify-'
}

"$runner" test-exit-ok 0 0 0 -- bash -c 'exit 0'
test "$?" -eq 0
"$runner" test-exit-fail 0 0 0 -- bash -c 'exit 7' && bad=0 || bad=$?
test "${bad:-0}" -eq 7
no_leftovers
printf '✅ exit status passes through and the cgroup is cleaned up on success and failure\n'

# A pids.max of 1 permits only the migrated process itself; any attempt to
# fork from inside it must fail.
if "$runner" test-pids-limit 1 0 0 -- bash -c '( : ) 2>/dev/null' 2>/dev/null; then
  echo "expected fork to be refused under pids.max=1" >&2
  exit 1
fi
no_leftovers
printf '✅ pids.max is enforced (fork refused) and cleaned up afterward\n'

# cpu.max and memory.max land in the group's control files while it runs.
"$runner" test-limits-visible 0 2000 256 -- bash -c '
  cg=$(grep -o "/pf-verify-test-limits-visible-[0-9]*" /proc/self/cgroup)
  test "$(cat "/sys/fs/cgroup${cg}/cpu.max")" = "200000 100000"
  test "$(cat "/sys/fs/cgroup${cg}/memory.max")" = "268435456"
'
no_leftovers
printf '✅ cpu.max (2000 milli -> 200000/100000) and memory.max (256 MiB) are written correctly\n'

printf '✅ run-in-cgroup.sh isolates, enforces, and cleans up correctly\n'

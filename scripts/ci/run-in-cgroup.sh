#!/usr/bin/env bash
# Runs a command inside its own, freshly created cgroup v2 leaf, giving it
# process-count/CPU/memory isolation from whatever else is running on the
# machine - without touching mount, network, or user namespaces the way
# this project's own `platform-factory pipeline run --sandbox require`
# would. That full sandbox pivots the stage into a private filesystem
# root and denies network by default, which would also require every
# verification stage that needs the docker/podman socket, /dev/kvm, the
# repo checkout, or the module cache to have all of that re-mounted in
# explicitly. Plain cgroup isolation gives the requested resource
# containment while leaving all of that host access alone.
#
# Usage: run-in-cgroup.sh NAME PIDS_MAX CPU_MILLI MEMORY_MIB -- CMD [ARGS...]
#   PIDS_MAX, CPU_MILLI, MEMORY_MIB: 0 to leave that limit unset.
#   CPU_MILLI is a millicore share, e.g. 4000 = 4 cores.
set -euo pipefail

if [ "$#" -lt 6 ] || [ "$5" != "--" ]; then
  echo "usage: $0 NAME PIDS_MAX CPU_MILLI MEMORY_MIB -- CMD [ARGS...]" >&2
  exit 2
fi
name=$1 pids_max=$2 cpu_milli=$3 memory_mib=$4
shift 5

base="${PF_CGROUP_ROOT:-/sys/fs/cgroup}"
[ -w "$base" ] || { echo "run-in-cgroup: $base is not writable (no cgroup v2 delegation available)" >&2; exit 1; }

group="$base/pf-verify-${name}-$$"
mkdir "$group"

if [ "$pids_max" -gt 0 ]; then
  echo "$pids_max" > "$group/pids.max"
fi
if [ "$cpu_milli" -gt 0 ]; then
  period=100000
  quota=$(( cpu_milli * period / 1000 ))
  printf '%d %d\n' "$quota" "$period" > "$group/cpu.max"
fi
if [ "$memory_mib" -gt 0 ]; then
  echo $(( memory_mib * 1024 * 1024 )) > "$group/memory.max"
fi

# The subshell migrates itself into the group before exec'ing the real
# command, so there is no fork-then-migrate race: cgroup membership is
# inherited across exec, and every descendant the command spawns lands in
# the same group from the start.
(
  echo "$BASHPID" > "$group/cgroup.procs"
  exec "$@"
) &
child=$!
status=0
wait "$child" || status=$?

# The kernel can take a moment after the last process exits to finish
# vacating the cgroup (rstat flushing/css offlining), so an immediate
# rmdir can transiently fail with EBUSY even though cgroup.procs is
# already empty. Retry briefly rather than leaking the directory.
for _ in 1 2 3 4 5 6 7 8 9 10; do
  rmdir "$group" 2>/dev/null && break
  sleep 0.2
done

# A command can leave behind a daemonized grandchild that detaches and
# outlives it (observed in practice: `dotnet publish` starts a persistent
# VBCSCompiler build-server process that reparents to PID 1 and never
# exits on its own). That keeps the group non-empty forever, so the
# retries above never succeed. Force it: cgroup.kill SIGKILLs every
# remaining member at once, which a plain, non-isolated script run would
# never have caught or contained in the first place.
if [ -d "$group" ]; then
  echo 1 > "$group/cgroup.kill" 2>/dev/null || true
  for _ in 1 2 3 4 5; do
    rmdir "$group" 2>/dev/null && break
    sleep 0.2
  done
fi

exit "$status"

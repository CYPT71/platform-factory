#!/usr/bin/env bash
# Validate recovery from a real network partition of a kind worker node -
# distinct from tests/kind/test-kind-worker-loss.sh, which stops the
# worker's container entirely (the process is gone, the node object must
# be explicitly deleted before anything reschedules). Here the worker
# container keeps running throughout; only its network connectivity is
# severed via a real `docker network disconnect` on the container engine's
# own bridge network, then restored. Two properties this proves that
# worker-loss does not: (1) Kubernetes reschedules a workload off a node
# it merely can't reach, not only one whose process has died, and (2) the
# node heals back to Ready on its own the moment connectivity returns -
# no node deletion, no re-registration, unlike a genuinely lost worker.
set -euo pipefail

namespace=${PLATFORM_FACTORY_KIND_PARTITION_NAMESPACE:-platform-factory-network-partition}
evidence_dir=${PLATFORM_FACTORY_EVIDENCE_DIR:-.}
container_engine=${PLATFORM_FACTORY_CONTAINER_ENGINE:-podman}
# kind names the bridge/CNI network its node containers share "kind"
# regardless of provider (docker or podman) - verified locally against a
# real docker-provider cluster; not independently re-verified here against
# podman, which this repo's own kind jobs otherwise use.
kind_network=${PLATFORM_FACTORY_KIND_NETWORK:-kind}

mapfile -t workers < <(
  kubectl get nodes -l node-role.kubernetes.io/worker \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sed '/^$/d'
)
test "${#workers[@]}" -ge 2
partitioned_worker=${workers[0]}
surviving_worker=${workers[1]}

kubectl create namespace "$namespace"
healed=false
cleanup() {
  if [ "$healed" != true ]; then
    "$container_engine" network connect "$kind_network" "$partitioned_worker" >/dev/null 2>&1 || true
  fi
  kubectl get nodes -o wide >"$evidence_dir/kind-partition-nodes-after.txt" 2>&1 || true
  kubectl get pods -n "$namespace" -o wide >"$evidence_dir/kind-partition-pods-after.txt" 2>&1 || true
  kubectl get events -n "$namespace" --sort-by=.lastTimestamp >"$evidence_dir/kind-partition-events.txt" 2>&1 || true
  kubectl delete namespace "$namespace" --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT

cat <<'EOF' | kubectl apply -n "$namespace" -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: resilient-network-partition
spec:
  replicas: 2
  selector:
    matchLabels: {app: resilient-network-partition}
  template:
    metadata:
      labels: {app: resilient-network-partition}
    spec:
      nodeSelector:
        node-role.kubernetes.io/worker: ""
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: ScheduleAnyway
          labelSelector:
            matchLabels: {app: resilient-network-partition}
      tolerations:
        - key: node.kubernetes.io/not-ready
          operator: Exists
          effect: NoExecute
          tolerationSeconds: 10
        - key: node.kubernetes.io/unreachable
          operator: Exists
          effect: NoExecute
          tolerationSeconds: 10
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.10
          imagePullPolicy: IfNotPresent
EOF

kubectl rollout status deployment/resilient-network-partition -n "$namespace" --timeout=120s
kubectl get nodes -o wide | tee "$evidence_dir/kind-partition-nodes-before.txt"
kubectl get pods -n "$namespace" -o wide | tee "$evidence_dir/kind-partition-pods-before.txt"
before=$(kubectl get pods -n "$namespace" -l app=resilient-network-partition \
  -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sed '/^$/d')
test "$(printf '%s\n' "$before" | sort -u | wc -l | tr -d ' ')" -eq 2

printf 'PARTITIONED_WORKER=%s\nSURVIVING_WORKER=%s\n' "$partitioned_worker" "$surviving_worker" \
  | tee "$evidence_dir/kind-partition-selection.txt"

# Sever the worker's network connectivity. Its container, and the kubelet
# inside it, keep running the entire time - this is a partition, not a
# process death.
"$container_engine" network disconnect "$kind_network" "$partitioned_worker" \
  | tee "$evidence_dir/kind-partition-disconnect.txt"

# The API server stops hearing from the partitioned kubelet and eventually
# marks it Unknown (not False - a partitioned node cannot report its own
# unhealthiness, it simply stops being heard from at all, which is a
# materially different condition than test-kind-worker-loss.sh's Ready=
# False/True check on a node whose process actually exited).
partitioned_ready=True
for _ in $(seq 1 90); do
  partitioned_ready=$(kubectl get "node/$partitioned_worker" \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')
  if [ "$partitioned_ready" = Unknown ]; then
    break
  fi
  sleep 2
done
test "$partitioned_ready" = Unknown
printf 'PARTITIONED_WORKER_READY=%s\n' "$partitioned_ready" \
  | tee "$evidence_dir/kind-partition-ready-during.txt"

# Without deleting anything, the replica the partitioned node was holding
# must be rescheduled onto the surviving worker once the toleration
# period elapses. Measured against a real cluster: eviction (taint-based,
# after this Deployment's own 10s tolerationSeconds on top of the ~40s
# node-monitor-grace-period that produced Unknown above) plus a fresh
# pause:3.10 pod reaching Running landed around 60-95s after the
# partition began.
#
# Deliberately count only pods on survivingWorker, not "every Running pod
# equals 2" - measured against a real cluster: the original pod on the
# partitioned node is left permanently reporting .status.phase=Running
# even after eviction, because kubectl's "Terminating" column is only
# metadata.deletionTimestamp being set, not an actual phase, and the
# partitioned node's kubelet can never report the real final phase back
# to an API server it can't reach. A field-selector on status.phase=
# Running therefore still matches it, so counting "2 total Running pods"
# never converges until the partition heals - the opposite of what this
# loop is supposed to observe before healing anything.
on_survivor=0
for _ in $(seq 1 150); do
  on_survivor=$(kubectl get pods -n "$namespace" -l app=resilient-network-partition \
    --field-selector=status.phase=Running \
    -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' \
    | grep -c "^${surviving_worker}\$" || true)
  if [ "$on_survivor" -eq 2 ]; then
    break
  fi
  sleep 2
done
test "$on_survivor" -eq 2
printf 'NETWORK_PARTITION_RESCHEDULE_OK replicas=2 survivor=%s\n' "$surviving_worker" \
  | tee "$evidence_dir/kind-partition-reschedule-result.txt"

# Heal the partition - the node itself, unlike a genuinely lost worker,
# must return to Ready on its own with no node deletion or re-registration.
"$container_engine" network connect "$kind_network" "$partitioned_worker" \
  | tee "$evidence_dir/kind-partition-reconnect.txt"
healed_ready=Unknown
for _ in $(seq 1 60); do
  healed_ready=$(kubectl get "node/$partitioned_worker" \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')
  if [ "$healed_ready" = True ]; then
    healed=true
    break
  fi
  sleep 2
done
test "$healed_ready" = True
printf 'NETWORK_PARTITION_HEALED_OK node=%s\n' "$partitioned_worker" \
  | tee "$evidence_dir/kind-partition-healed-result.txt"

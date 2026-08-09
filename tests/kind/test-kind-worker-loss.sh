#!/usr/bin/env bash
# Validate Kubernetes recovery after a real kind worker container disappears.
# This is control-plane evidence, not a platform-factory MicroVM migration claim.
set -euo pipefail

namespace=${PLATFORM_FACTORY_KIND_LOSS_NAMESPACE:-platform-factory-worker-loss}
evidence_dir=${PLATFORM_FACTORY_EVIDENCE_DIR:-.}
test "$(kubectl get runtimeclass platform-factory -o jsonpath='{.handler}')" = platform-factory
mapfile -t workers < <(
  kubectl get nodes -l node-role.kubernetes.io/worker \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sed '/^$/d'
)
test "${#workers[@]}" -ge 2
lost_worker=${workers[0]}
surviving_worker=${workers[1]}

kubectl create namespace "$namespace"
cleanup() {
  kubectl get nodes -o wide >"$evidence_dir/kind-worker-loss-nodes-after.txt" 2>&1 || true
  kubectl get pods -n "$namespace" -o wide >"$evidence_dir/kind-worker-loss-pods-after.txt" 2>&1 || true
  kubectl get events -n "$namespace" --sort-by=.lastTimestamp >"$evidence_dir/kind-worker-loss-events.txt" 2>&1 || true
  kubectl delete namespace "$namespace" --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT

cat <<'EOF' | kubectl apply -n "$namespace" -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: resilient-default-runtime
spec:
  replicas: 2
  selector:
    matchLabels: {app: resilient-default-runtime}
  template:
    metadata:
      labels: {app: resilient-default-runtime}
    spec:
      nodeSelector:
        node-role.kubernetes.io/worker: ""
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: ScheduleAnyway
          labelSelector:
            matchLabels: {app: resilient-default-runtime}
      tolerations:
        - key: node.kubernetes.io/not-ready
          operator: Exists
          effect: NoExecute
          tolerationSeconds: 5
        - key: node.kubernetes.io/unreachable
          operator: Exists
          effect: NoExecute
          tolerationSeconds: 5
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.10
          imagePullPolicy: IfNotPresent
EOF

kubectl rollout status deployment/resilient-default-runtime -n "$namespace" --timeout=120s
kubectl get nodes -o wide | tee "$evidence_dir/kind-worker-loss-nodes-before.txt"
kubectl get pods -n "$namespace" -o wide | tee "$evidence_dir/kind-worker-loss-pods-before.txt"
before=$(kubectl get pods -n "$namespace" -l app=resilient-default-runtime \
  -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sed '/^$/d')
test "$(printf '%s\n' "$before" | sort -u | wc -l | tr -d ' ')" -eq 2

printf 'LOST_WORKER=%s\nSURVIVING_WORKER=%s\n' "$lost_worker" "$surviving_worker" \
  | tee "$evidence_dir/kind-worker-loss-selection.txt"
container_engine=${PLATFORM_FACTORY_CONTAINER_ENGINE:-podman}
test "$container_engine" = podman
"$container_engine" stop "$lost_worker" | tee "$evidence_dir/kind-worker-loss-podman-stop.txt"
# A vanished kubelet transitions through Ready=Unknown on current Kubernetes
# releases; older releases commonly reported Ready=False. Both mean the node
# is unavailable. Waiting specifically for False made the proof flaky for two
# minutes even though the worker container was already stopped.
lost_ready=True
for _ in $(seq 1 90); do
  lost_ready=$(kubectl get "node/$lost_worker" \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')
  if [ "$lost_ready" != True ]; then
    break
  fi
  sleep 2
done
test "$lost_ready" != True
printf 'LOST_WORKER_READY=%s\n' "$lost_ready" \
  | tee "$evidence_dir/kind-worker-loss-ready.txt"
# Once the kubelet loss has been observed, remove its stale API object. This
# models the controller/operator reconciliation that follows permanent node
# loss and avoids depending on the cluster-wide default pod eviction timeout.
kubectl delete "node/$lost_worker" \
  | tee "$evidence_dir/kind-worker-loss-node-delete.txt"

after=""
for _ in $(seq 1 90); do
  after=$(kubectl get pods -n "$namespace" -l app=resilient-default-runtime \
    --field-selector=status.phase=Running \
    -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sed '/^$/d')
  if [ "$(printf '%s\n' "$after" | sed '/^$/d' | wc -l | tr -d ' ')" -eq 2 ] &&
     [ "$(printf '%s\n' "$after" | sort -u)" = "$surviving_worker" ]; then
    break
  fi
  sleep 2
done
test "$(printf '%s\n' "$after" | wc -l | tr -d ' ')" -eq 2
test "$(printf '%s\n' "$after" | sort -u)" = "$surviving_worker"
printf 'WORKER_LOSS_RECOVERY_OK replicas=2 survivor=%s runtime=default\n' "$surviving_worker" \
  | tee "$evidence_dir/kind-worker-loss-result.txt"

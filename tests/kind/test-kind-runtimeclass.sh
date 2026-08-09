#!/usr/bin/env bash
# Prove RuntimeClass selection and scheduling on a real multi-node kind cluster.
# This is a configuration/scheduler contract: kind workers do not expose KVM
# and intentionally do not install platform-factory-runtime.
set -euo pipefail

namespace=${PLATFORM_FACTORY_KIND_NAMESPACE:-platform-factory-runtime-contract}
runtimeclass=${PLATFORM_FACTORY_RUNTIMECLASS_MANIFEST:-platform-factory-runtimeclass.yaml}
evidence_dir=${PLATFORM_FACTORY_EVIDENCE_DIR:-.}

control_count=$(kubectl get nodes -l node-role.kubernetes.io/control-plane \
  -o jsonpath='{.items[*].metadata.name}' | wc -w | tr -d ' ')
test "$control_count" -ge 1

# kind intentionally leaves worker nodes with ROLE "<none>". Discover every
# non-control-plane node, then apply the conventional worker label used by the
# scheduling contract below.
worker_nodes=""
control_nodes=" $(kubectl get nodes -l node-role.kubernetes.io/control-plane \
  -o jsonpath='{.items[*].metadata.name}') "
for node in $(kubectl get nodes -o jsonpath='{.items[*].metadata.name}'); do
  case "$control_nodes" in
    *" $node "*) ;;
    *) worker_nodes="$worker_nodes $node" ;;
  esac
done
worker_count=$(printf '%s\n' "$worker_nodes" | wc -w | tr -d ' ')
test "$worker_count" -ge 2
for node in $worker_nodes; do
  kubectl label node "$node" node-role.kubernetes.io/worker= --overwrite
done
kubectl wait --for=condition=Ready node --all --timeout=90s
kubectl get nodes -o wide | tee "$evidence_dir/kind-multinode-nodes.txt"

kubectl apply -f "$runtimeclass"
test "$(kubectl get runtimeclass platform-factory -o jsonpath='{.handler}')" = platform-factory
kubectl get runtimeclass platform-factory -o yaml \
  >"$evidence_dir/platform-factory-runtimeclass-live.yaml"

kubectl create namespace "$namespace"
cleanup() {
  kubectl get pods -n "$namespace" -o wide \
    >"$evidence_dir/kind-runtimeclass-pods.txt" 2>&1 || true
  kubectl get events -n "$namespace" --sort-by=.lastTimestamp \
    >"$evidence_dir/kind-runtimeclass-events.txt" 2>&1 || true
  kubectl delete namespace "$namespace" --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT

cat <<'EOF' | kubectl apply -n "$namespace" -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: default-runtime
spec:
  replicas: 2
  selector:
    matchLabels: {app: default-runtime}
  template:
    metadata:
      labels: {app: default-runtime}
    spec:
      nodeSelector:
        node-role.kubernetes.io/worker: ""
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: DoNotSchedule
          labelSelector:
            matchLabels: {app: default-runtime}
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.10
          imagePullPolicy: IfNotPresent
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: platform-factory-selection
spec:
  replicas: 2
  selector:
    matchLabels: {app: platform-factory-selection}
  template:
    metadata:
      labels: {app: platform-factory-selection}
    spec:
      runtimeClassName: platform-factory
      nodeSelector:
        node-role.kubernetes.io/worker: ""
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: DoNotSchedule
          labelSelector:
            matchLabels: {app: platform-factory-selection}
      containers:
        - name: must-use-platform-factory
          image: registry.k8s.io/pause:3.10
          imagePullPolicy: IfNotPresent
EOF

kubectl rollout status deployment/default-runtime -n "$namespace" --timeout=120s

for _ in $(seq 1 60); do
  scheduled=$(kubectl get pods -n "$namespace" -l app=platform-factory-selection \
    -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sed '/^$/d')
  if [ "$(printf '%s\n' "$scheduled" | sed '/^$/d' | wc -l | tr -d ' ')" -eq 2 ] &&
     [ "$(printf '%s\n' "$scheduled" | sort -u | wc -l | tr -d ' ')" -eq 2 ]; then
    break
  fi
  sleep 2
done

scheduled=$(kubectl get pods -n "$namespace" -l app=platform-factory-selection \
  -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sed '/^$/d')
test "$(printf '%s\n' "$scheduled" | wc -l | tr -d ' ')" -eq 2
test "$(printf '%s\n' "$scheduled" | sort -u | wc -l | tr -d ' ')" -eq 2

# Scheduling must succeed, but execution must fail closed because this job has
# deliberately not installed the runtime handler or exposed /dev/kvm.
if kubectl wait -n "$namespace" -l app=platform-factory-selection \
  --for=condition=Ready pod --timeout=5s; then
  echo "platform-factory pods unexpectedly became Ready without the node runtime" >&2
  exit 1
fi
sleep 5
kubectl get pods -n "$namespace" -o wide \
  | tee "$evidence_dir/kind-runtimeclass-pods.txt"
kubectl get events -n "$namespace" --sort-by=.lastTimestamp \
  | tee "$evidence_dir/kind-runtimeclass-events.txt"
grep -Eiq 'platform-factory|runtime.*(not|no|failed)|sandbox' \
  "$evidence_dir/kind-runtimeclass-events.txt"

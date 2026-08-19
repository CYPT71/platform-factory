#!/usr/bin/env bash
# Prove remote cancellation and restart durability across two real kind nodes.
set -euo pipefail

namespace=${PLATFORM_FACTORY_KIND_CANCEL_NAMESPACE:-platform-factory-cancellation}
evidence_dir=${PLATFORM_FACTORY_EVIDENCE_DIR:-.}
image=localhost/platform-factory-distributed:test
cluster_name=${PLATFORM_FACTORY_KIND_CLUSTER_NAME:-platform-factory-distributed}
build_dir=$(mktemp -d)
container_engine=${PLATFORM_FACTORY_CONTAINER_ENGINE:-podman}
if [ "$container_engine" != podman ]; then echo "kind tests require Podman" >&2; exit 1; fi
if ! command -v "$container_engine" >/dev/null 2>&1; then echo "podman is required" >&2; exit 1; fi
kind_bin=${KIND_BIN:-$(command -v kind || true)}
if [ -z "$kind_bin" ]; then
  kind_bin=$(go env GOPATH)/bin/kind
fi
if [ -z "$kind_bin" ]; then echo "kind is required" >&2; exit 1; fi
if [ ! -x "$kind_bin" ]; then echo "kind is required at $kind_bin" >&2; exit 1; fi
port_forward_pid=
cleanup() {
  if [ -n "$port_forward_pid" ]; then kill "$port_forward_pid" >/dev/null 2>&1 || true; fi
  kubectl logs -n "$namespace" deployment/distributed-worker >"$evidence_dir/kind-cancellation-worker.log" 2>&1 || true
  kubectl logs -n "$namespace" deployment/distributed-control >"$evidence_dir/kind-cancellation-control.log" 2>&1 || true
  kubectl get pods -n "$namespace" -o wide >"$evidence_dir/kind-cancellation-pods.txt" 2>&1 || true
  kubectl delete namespace "$namespace" --wait=false >/dev/null 2>&1 || true
  rm -rf "$build_dir"
}
trap cleanup EXIT

mapfile -t workers < <(kubectl get nodes -l node-role.kubernetes.io/worker -o name | sed 's#node/##')
test "${#workers[@]}" -ge 2
control_node=${workers[0]}
worker_node=${workers[1]}
target_arch=$(kubectl get node "$control_node" -o jsonpath='{.status.nodeInfo.architecture}')
case "$target_arch" in amd64|arm64) ;; *) echo "unsupported kind node architecture: $target_arch" >&2; exit 1 ;; esac

CGO_ENABLED=0 GOOS=linux GOARCH="$target_arch" go build -trimpath -o "$build_dir/platform-factory-control-plane" ./cmd/platform-factory-control-plane
CGO_ENABLED=0 GOOS=linux GOARCH="$target_arch" go build -trimpath -o "$build_dir/platform-factory-worker" ./cmd/platform-factory-worker
cp scripts/ci/Dockerfile.distributed-control "$build_dir/Dockerfile"
"$container_engine" build -t "$image" "$build_dir"
"$container_engine" save --format docker-archive --output "$build_dir/distributed-image.tar" "$image"
"$kind_bin" load image-archive --name "$cluster_name" "$build_dir/distributed-image.tar"

openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=platform-factory-kind-ca' \
  -keyout "$build_dir/ca-key.pem" -out "$build_dir/ca.pem" >/dev/null 2>&1
make_cert() {
  local name=$1 organization=$2 extensions=$3
  openssl req -newkey rsa:2048 -nodes -subj "/CN=$name/O=$organization" \
    -keyout "$build_dir/$name-key.pem" -out "$build_dir/$name.csr" >/dev/null 2>&1
  openssl x509 -req -days 1 -in "$build_dir/$name.csr" \
    -CA "$build_dir/ca.pem" -CAkey "$build_dir/ca-key.pem" -CAcreateserial \
    -extfile <(printf '%s\n' "$extensions") -out "$build_dir/$name.pem" >/dev/null 2>&1
}
make_cert server worker 'subjectAltName=DNS:distributed-control,IP:127.0.0.1'
make_cert worker-kind worker 'extendedKeyUsage=clientAuth'
make_cert operator-kind worker 'extendedKeyUsage=clientAuth'

kubectl create namespace "$namespace"
kubectl create secret generic distributed-tls -n "$namespace" \
  --from-file=ca.pem="$build_dir/ca.pem" \
  --from-file=server.pem="$build_dir/server.pem" --from-file=server-key.pem="$build_dir/server-key.pem" \
  --from-file=worker.pem="$build_dir/worker-kind.pem" --from-file=worker-key.pem="$build_dir/worker-kind-key.pem"
cat <<EOF | kubectl apply -n "$namespace" -f -
apiVersion: v1
kind: Service
metadata: {name: distributed-control}
spec:
  selector: {app: distributed-control}
  ports: [{name: https, port: 8443, targetPort: 8443}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: distributed-control}
spec:
  replicas: 1
  selector: {matchLabels: {app: distributed-control}}
  template:
    metadata: {labels: {app: distributed-control}}
    spec:
      nodeName: $control_node
      containers:
        - name: control
          image: $image
          imagePullPolicy: Never
          securityContext: {runAsUser: 0, runAsGroup: 0}
          command: [/platform-factory-control-plane]
          args: [-listen=:8443, -cert=/tls/server.pem, -key=/tls/server-key.pem, -ca=/tls/ca.pem, -state-file=/state/control.json, -audit-file=/state/audit.jsonl]
          ports: [{containerPort: 8443}]
          volumeMounts: [{name: tls, mountPath: /tls, readOnly: true}, {name: state, mountPath: /state}]
      volumes:
        - {name: tls, secret: {secretName: distributed-tls}}
        - {name: state, hostPath: {path: /var/lib/platform-factory-cancellation, type: DirectoryOrCreate}}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: distributed-worker}
spec:
  replicas: 0
  selector: {matchLabels: {app: distributed-worker}}
  template:
    metadata: {labels: {app: distributed-worker}}
    spec:
      nodeName: $worker_node
      containers:
        - name: worker
          image: $image
          imagePullPolicy: Never
          command: [/platform-factory-worker]
          args: [-control-plane=https://distributed-control:8443, -cert=/tls/worker.pem, -key=/tls/worker-key.pem, -ca=/tls/ca.pem, -platform=linux/$target_arch, -poll-interval=100ms, -heartbeat-interval=100ms, -simulated-execution-duration=30s, -demo-simulate]
          volumeMounts: [{name: tls, mountPath: /tls, readOnly: true}]
      volumes: [{name: tls, secret: {secretName: distributed-tls}}]
EOF
kubectl rollout status deployment/distributed-control -n "$namespace" --timeout=120s
kubectl scale deployment/distributed-worker -n "$namespace" --replicas=1
kubectl rollout status deployment/distributed-worker -n "$namespace" --timeout=120s

kubectl port-forward -n "$namespace" service/distributed-control 18443:8443 >"$evidence_dir/kind-cancellation-port-forward.log" 2>&1 &
port_forward_pid=$!
for _ in $(seq 1 30); do
  curl --silent --fail --cacert "$build_dir/ca.pem" --cert "$build_dir/operator-kind.pem" \
    --key "$build_dir/operator-kind-key.pem" https://127.0.0.1:18443/workers >/dev/null && break
  sleep 1
done
api() {
  curl --silent --show-error --fail --cacert "$build_dir/ca.pem" --cert "$build_dir/operator-kind.pem" \
    --key "$build_dir/operator-kind-key.pem" "$@"
}
submit=$(api -H 'content-type: application/json' --data "{\"payload\":\"cancel across nodes\",\"required_platform\":\"linux/$target_arch\"}" https://127.0.0.1:18443/lease/submit)
lease_id=$(printf '%s' "$submit" | jq -er .lease_id)
for _ in $(seq 1 50); do
  state=$(api "https://127.0.0.1:18443/lease/status?id=$lease_id" | jq -r .state)
  [ "$state" = assigned ] && break
  sleep .1
done
test "$state" = assigned
cancel=$(api -H 'content-type: application/json' --data "{\"lease_id\":\"$lease_id\"}" https://127.0.0.1:18443/lease/cancel)
test "$(printf '%s' "$cancel" | jq -r .canceled)" = true
sleep 1
test "$(api "https://127.0.0.1:18443/lease/status?id=$lease_id" | jq -r .state)" = canceled

old_pod=$(kubectl get pod -n "$namespace" -l app=distributed-control -o jsonpath='{.items[0].metadata.name}')
kubectl delete pod -n "$namespace" "$old_pod" --wait=true
kubectl rollout status deployment/distributed-control -n "$namespace" --timeout=120s
kill "$port_forward_pid" >/dev/null 2>&1 || true
wait "$port_forward_pid" 2>/dev/null || true
kubectl port-forward -n "$namespace" service/distributed-control 18443:8443 >>"$evidence_dir/kind-cancellation-port-forward.log" 2>&1 &
port_forward_pid=$!
for _ in $(seq 1 30); do
  if api https://127.0.0.1:18443/workers >/dev/null; then break; fi
  sleep 1
done
test "$(api "https://127.0.0.1:18443/lease/status?id=$lease_id" | jq -r .state)" = canceled
printf 'DISTRIBUTED_CANCELLATION_OK lease=%s control_node=%s worker_node=%s\n' "$lease_id" "$control_node" "$worker_node" \
  | tee "$evidence_dir/kind-cancellation-result.txt"

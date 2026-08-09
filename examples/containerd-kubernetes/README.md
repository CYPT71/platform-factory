# containerd and Kubernetes RuntimeClass

This is an administrator example. It requires Linux nodes, containerd v2,
`kubectl`, permission to update containerd configuration, and KVM for a real
guest boot.

Generate the containerd CRI fragment and Kubernetes `RuntimeClass` from the
same shipped helper used by CI:

```sh
go run ./plugins/containerd/cmd/platform-factory-containerd config > /tmp/platform-factory-containerd.toml
go run ./plugins/containerd/cmd/platform-factory-containerd runtimeclass > /tmp/platform-factory-runtimeclass.yaml
kubectl apply -f /tmp/platform-factory-runtimeclass.yaml
```

The checked-in [`runtimeclass.yaml`](runtimeclass.yaml) also contains a
minimal opt-in Pod. Replace the `ghcr.io/replace-me/...` image with your image
reference ending in `@sha256:<64 hex>`, then apply it:

```sh
kubectl apply -f examples/containerd-kubernetes/runtimeclass.yaml
kubectl get pod platform-factory-example -o wide
kubectl logs platform-factory-example
kubectl get pod platform-factory-example -o jsonpath='{.status.containerStatuses[0].state}'
kubectl delete pod platform-factory-example
```

Omit `runtimeClassName` to run an ordinary container with the node's default
runtime. Keep it to select the platform-factory shim and its native KVM MicroVM.
The handler fails closed on nodes where the shim or `/dev/kvm` is unavailable;
schedule production workloads only onto labeled, runtime-enabled nodes.

Install `containerd-shim-platform-factory-v1` on every eligible node before using
`runtimeClassName: platform-factory`. The multi-node proof is
`scripts/microvm/test-kind-runtimeclass.sh`; the hardware boot proof also
requires KVM on the selected node.

Expected result: Pods declaring `runtimeClassName: platform-factory` are routed to
the platform-factory shim, while the node's default runtime remains unchanged.

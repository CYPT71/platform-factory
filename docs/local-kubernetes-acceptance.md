# Real signed Kubernetes delivery and recovery

The `signed-plugin-lifecycle` job in `ci-kind-multinode.yml` creates a disposable
single-node kind cluster and a private loopback registry. It executes the real
CLI and signed Kubernetes plugin, rather than a simulated API client.

## Acceptance contract

`tests/kind/test-kind-signed-lifecycle.sh` proves:

1. Build the same Linux service twice and compare the OCI layouts.
2. Generate native SBOM/provenance and cryptographically signed evidence.
3. Publish with the production evidence policy to a local registry.
4. Deploy the published immutable digest through the signed plugin; verify the
   running service's `/version` endpoint, logs and events.
5. Scale the same image from one to two replicas. Replay the identical request
   using an explicit operation key; reject different parameters under that key.
6. Publish/deploy v2, roll back to v1 and check its HTTP response.
7. Redeploy the previously used v2 digest, roll back again and check v1 again.
8. Retrieve the associated SBOM, provenance and signature from the registry and
   verify each blob's digest/size and each manifest's subject association.

The last check uses the referrers API when available and the OCI referrers tag
schema otherwise. Artifact publication uses the final OCI 1.1 image manifest
format with an empty JSON config, layers, `artifactType` and `subject`, not the
obsolete draft artifact-manifest media type. See the
[OCI specification](https://github.com/opencontainers/distribution-spec/blob/main/spec.md#pushing-manifests-with-subject).
Fallback index writes preserve existing descriptors and metadata, reject
non-index content and verify the stored index. Serialize independent publishers
for the same subject when relying on fallback tags: a mutable registry tag is
not a cross-process transaction.

## Run against a disposable local cluster

Use the setup from the CI job for a kind node and a registry whose port is
bound only to loopback. Its containerd `hosts.toml` must map the image reference
`localhost:PORT` to the registry container on the kind network. The workflow
pins both container images by digest.

```sh
export KUBECONFIG=/path/to/isolated/kubeconfig
export PLATFORM_FACTORY_KIND_CONTEXT=kind-platform-factory-lifecycle
export PLATFORM_FACTORY_ACCEPTANCE_REGISTRY=localhost:5000
export PLATFORM_FACTORY_EVIDENCE_DIR="$PWD/dist/kind-signed-lifecycle"
tests/kind/test-kind-signed-lifecycle.sh
```

The script refuses a non-kind context, a missing explicit kubeconfig and a
non-loopback registry. It creates its own namespace and temporary signing keys,
then removes the namespace and private workspace on exit. Evidence contains
receipts, registry indexes, pod/events summaries and HTTP observations; no raw
kubeconfig or private signing key is copied into it. It does not delete a
caller-owned cluster or registry.

On a host without plugin sandbox support, explicitly set
`PLATFORM_FACTORY_ACCEPTANCE_ALLOW_UNSANDBOXED=1` for this functional test.
The CI job makes that test-only choice explicitly; this scenario does not
replace the dedicated isolation/security workflows or change production
sandbox defaults.

Local validation on 2026-10-24 used a real Linux/arm64 kind node inside the
existing AppleHV Podman VM on macOS. Kind ran with the VM's own Podman client
because the host Podman 6 client and VM Podman 5 server were incompatible for
kind's label-listing template. The kubeconfig was exported to a private local
file without changing the user's current context. This proves a container
workload on Kubernetes in the Apple-backed Linux VM; it does not claim that
this scenario boots the workload as a Platform Factory microVM.

## New request versus explicit retry

`deploy` and `rollback` now generate a fresh operation ID per invocation. A
new scale, redeploy or second rollback must not be mistaken for a previously
completed mutation. The ID is printed to stderr; deploy JSON also returns it
as `operation_id`.

For deliberate replay, pass that exact ID with `--operation-id ID` and keep
the same target cluster and request parameters. Changed parameters under an
existing key fail closed. A failed or indeterminate journal entry still
requires inspecting/reconciling external state before another mutation;
issuing a new key is not a substitute for that recovery decision.

Deployment receipts now preserve `runtime_class` and recognize every workload
kind rendered by `deploy`, so `logs` and `events` can read the receipt written
by the actual command. Historical receipts without this optional field remain
readable. Rollback remains limited to service Deployments.

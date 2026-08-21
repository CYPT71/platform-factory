# TODO review

Verified against the working tree and executable tests on 2026-08-11.

## Result

The repository MVP is valid, but the full long-term roadmap is not complete.
Checkboxes below are classified by executable evidence; an unchecked roadmap
item is not converted to done merely because a lower-level primitive exists.

| Source | Status | Review |
|---|---:|---|
| `mvp.md` | 72 done, 2 partial | Stabilization MVP is implemented; application-layer extraction and default workspace confinement remain partial. |
| `Pf-init.md` | Junior Go MVP done | Real Docker and Podman launch pass. Python/Node/.NET inspection and native execution pass; their verified Linux OCI runtime layers remain open. |
| `upgrade.md` | P0 partial, P1/P2 open | Honest Python preflight is implemented, but the complete Python registry/Kubernetes loop is not. |
| `Supreme-Graal.md` | 255 done, 0 open | External A↔B/B↔A now includes inspect/export/import, fail-closed loading, canonical external dependencies/transformations, secret-free boundaries and malformed-native-data rejection. |
| `Sanetizer-todo.md` | historical/stale in places | The recorded `assemble/project → internal/oci` boundary debt is now closed and enforced by architecture tests. Later progress notes supersede older unchecked prose. |
| Code TODOs | closed | Command context now reaches project builds and OCI events; stale implementation markers were removed. |
| Operational TBDs | intentionally open | Secondary on-call and Windows maintainer require human ownership; code cannot truthfully invent names. |

## MVP acceptance evidence

- `go test ./...` passes.
- Every separate Go workspace module passes.
- Strict real Docker and Podman Hello World acceptance tests pass.
- `demo/validate.sh docker` and `demo/validate.sh podman` start from a project
  containing only `main.go`, exercise the real CLI/plugin, retain a verified
  OCI layout, and assert the container output.
- `demo/try-pf.sh docker|podman` prepares the same isolated repository and
  opens an interactive shell that invites the user to type and explore the
  actual `pf` commands rather than watching a scripted transcript.
- `.github/workflows/ci-pf-init-experience.yml` makes missing real engines a
  failure rather than a green skip.
- `demo/validate-personas.sh` proves the Python SDK plugin, OCI build, and
  senior pipeline experiences from isolated clean workspaces.
- Signed external migration plugins pass A↔B export/import, artifact
  verification, rediscovery, reconciliation, durable evidence, crash refusal,
  and fuzz tests.
- Generic framed-RPC bundles use managed `plugin install/remove`: digest and
  signature verification precede an atomic, revalidated installation.

## Open product work that must remain visible

1. Provide trusted Linux runtime layers for interpreted/.NET language plugins.
2. Complete the Python build → evidence → registry → Kubernetes acceptance
   path from `upgrade.md`.
3. Persist workload choice and connect publish/deploy/status/logs/rollback at
   the project level.
4. Add workflow provenance, crash/reconnect/partial-success fault injection,
   and the remaining fuzz targets.

Terraform and Ansible remain intentionally deferred plugins and are not MVP
blockers.

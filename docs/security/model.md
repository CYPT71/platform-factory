# Security model

Inputs must be a regular executable and the output must not already
exist. Entrypoints must be clean absolute container paths; label keys
cannot be duplicated. Files are written to a fresh temporary directory
and atomically renamed to avoid partial output. SHA-256 identifies every
blob. Tar and gzip timestamps, gzip OS, and the default `created` value
are fixed, so identical input bytes and options produce identical
layouts.

The builder never reads or copies credentials, source trees, environment
variables, or host CA files. TLS/mTLS labels are metadata only. **No OCI
image configuration can enforce `no_new_privileges` or a read-only root
filesystem**: enforce those at runtime, for example `docker run
--read-only --security-opt no-new-privileges ...`, Kubernetes
`securityContext.readOnlyRootFilesystem: true` and
`allowPrivilegeEscalation: false`, or equivalent containerd policy.

This is a summary. For the full asset/actor/trust-boundary model, the
threat and control register (T01-T30), and documented residual risks, see
the [Threat Model](https://github.com/CYPT71/platform-factory/wiki/Threat-Model-and-Residual-Risks).
For which security-relevant capabilities are stable versus still
experimental, see the [maturity matrix](../reference/maturity.md).

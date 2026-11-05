# Security model

Inputs must be a regular executable and the output must not already
exist. Entrypoints must be clean absolute container paths; label keys
cannot be duplicated. Files are written to a fresh temporary directory
and atomically renamed to avoid partial output. SHA-256 identifies every
blob. Tar and gzip timestamps, gzip OS, and the default `created` value
are fixed, so identical input bytes and options produce identical
layouts.

The low-level builder packages explicitly supplied inputs. It does not scan
the host for credentials, source trees or CA files. Explicit extra files,
plugin layers and `system_files` can include caller-selected content; callers
must review those inputs. An explicitly configured CA bundle is supported,
and configured environment entries become OCI metadata. TLS/mTLS labels are
metadata only. **No OCI
image configuration can enforce `no_new_privileges` or a read-only root
filesystem**: enforce those at runtime, for example `docker run
--read-only --security-opt no-new-privileges ...`, Kubernetes
`securityContext.readOnlyRootFilesystem: true` and
`allowPrivilegeEscalation: false`, or equivalent containerd policy.

Capability-based RPC plugins require a verified executable digest and, by
default, a trusted manifest signature and Linux process sandbox. A failed or
unavailable sandbox is an error unless the caller explicitly permits
unsandboxed execution through trust policy. Namespace setup does not prove
that operation-specific grants were enforced: artifact operations also require
independently measured grant evidence. Plugin-family resource profiles request
memory, CPU-time and process limits; `Setrlimit` failures are logged rather
than fatal. These guarantees must not be inferred for plain-executable
language plugins, which use a [different interface](../language-plugin-layers.md).

This is a summary. For the full asset/actor/trust-boundary model, the
threat and control register (T01-T30), and documented residual risks, see
the [Threat Model](https://github.com/CYPT71/platform-factory/wiki/Threat-Model-and-Residual-Risks).
For which security-relevant capabilities are stable versus still
experimental, see the [maturity matrix](../reference/maturity.md).

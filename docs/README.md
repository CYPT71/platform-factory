# Documentation

Choose a task below. The CLI guide owns command usage; architecture documents
explain source boundaries; compatibility and maturity documents describe limits
and evidence. Runnable examples are indexed in [examples](../examples/README.md).

## Build and run

| Task | Start here | Check first |
| --- | --- | --- |
| Install the CLI and official plugins | [Distribution](distribution.md) | Target OS, Go toolchain and native-runtime prerequisites |
| Initialize, freeze and build a project | [CLI guide](cli-guide.md) | Locked inputs and packaged runtime for interpreted profiles |
| Inspect a language or add dependency layers | [Language plugins](language-plugin-layers.md) | Plugin subcommands differ; Go implements inspection only |
| Package a binary or understand image contents | [OCI architecture](architecture/oci-layout.md#oci-image-layout) | Linux ELF target and complete dynamic-library closure |
| Run a container or microVM | [CLI runtime guide](cli-guide.md#microvm-support) | [Host/backend compatibility](reference/compatibility.md) |
| Package locked inputs in GitLab CI | [GitLab CI guide](gitlab-ci-microvm.md) | Explicit input and boot pins |

## Publish, deploy and recover

| Task | Start here | Check first |
| --- | --- | --- |
| Publish an image with evidence | [CLI publication flow](cli-guide.md#publish-and-deploy) | [Registry compatibility](registry-providers.md) and evidence policy |
| Activate a Kubernetes or KubeVirt plugin | [Signed bundles](plugin-bundles.md) | Trusted publisher and explicit execution permissions |
| Assess Kubernetes delivery | [Cluster acceptance](local-kubernetes-acceptance.md) | Real cluster evidence is separate from offline conformance |
| Select the microVM Kubernetes runtime | [containerd integration](containerd-kubernetes.md) | Linux/amd64 KVM node and installed shim |
| Recover an interrupted publication | [Recovery procedure](cli-guide.md#recovering-an-interrupted-publication) | Reconcile external state before retrying a mutation |
| Operate distributed workers | [Distributed builds](distributed-builds.md) | mTLS identities, fresh workspaces and current CAS limitations |
| Diagnose a failure | [Limitations](limitations.md), [incident response](runbooks/incident-response.md) | Start with the failed command and its retained evidence |

## Extend and maintain

- [Source map](architecture/oci-layout.md#source-map): find the package owning a behavior.
- [API compatibility](api-compatibility.md): public wire formats, SDKs and migration rules.
- [MCP server](mcp.md): repository tools and client integration.
- [Plugin marketplace](plugin-marketplace.md): publishing and installing releases.
- [Security model](security/model.md): trust boundaries and enforcement limits.
- [Technology toolkit](technology-toolkit.md): offline analyzers and their contracts.
- [Contribution guide](../CONTRIBUTING.md): development and review workflow.

## Read evidence at the right level

[Capability maturity](reference/maturity.md) describes implementation readiness
using Stable, Beta, Alpha and Stub.
[Release support](maturity.md) separately uses Supported, Experimental and Stable
criteria to describe the supported adoption path. Neither label is a guarantee
that every host, backend or provider works.

A configured workflow describes what CI intends to test. Acceptance evidence
requires a completed run for the relevant version, target and environment.
Compilation, offline conformance, mocked tests and real guest/cluster execution
prove different things; consult the [compatibility matrix](reference/compatibility.md)
and [coverage scope](ci-coverage.md) before generalizing a result.

The [active stabilisation record](stabilisation.md) distinguishes implemented fixes
from the acceptance gates still required before experimental features become Stable.

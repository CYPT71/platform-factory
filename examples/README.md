# Examples and tutorials

New here? Start with [First platform-factory image in ten minutes](QUICKSTART.md).
It builds, verifies and inspects an OCI layout without Docker, Podman or KVM.

After that, choose the user goal closest to yours. Each major capability has
one canonical example and explains its prerequisites and expected result:

Every major example directory has an executable `run.sh`. It resolves the
repository from its own location, creates and cleans an isolated work
directory, and can therefore be launched from any current directory. Run the
portable set with `examples/run-all.sh`; hardware and cluster examples expose
the same entrypoint but fail early when their documented prerequisites are
missing.

| Capability | Example | Verification |
| --- | --- | --- |
| OCI layout construction | [`QUICKSTART.md`](QUICKSTART.md) and [`platform-factory.json`](platform-factory.json) | Produce and inspect a local OCI layout with only Go |
| Project detection, freeze and launch | [`project-config`](project-config) | `platform-factory plan --config examples/project-config/.config_image.yaml examples/project-config` |
| DAG, sandbox, cache and image assembly | [`pipeline.json`](pipeline.json) | `platform-factory pipeline plan examples/pipeline.json` |
| Language/runtime profiles | [`profile-node.json`](profile-node.json), [`profile-python.json`](profile-python.json), [`profile-java.json`](profile-java.json), [`profile-dotnet.json`](profile-dotnet.json) | `platform-factory build --config examples/profile-node.json -o /tmp/node-layout NODE_AND_APP_BUNDLE` |
| Language extensions | [`sdk`](sdk#plugin-sdks) | Validate Go, Python, JS/TS or C# with one conformance suite |
| Embed platform-factory from Go | [`sdk`](sdk) | Compile pipeline, MicroVM/VMM and plugin SDK applications |
| SBOM, provenance, signing and policy | [`supply-chain`](supply-chain) | Generate evidence and preview a protected publication |
| Native KVM/HVF MicroVM | [`microvm`](microvm) | Probe the host and manage a VM without QEMU |
| Podman-owned MicroVM lifecycle | [`podman-microvm`](podman-microvm) | Keep container and MicroVM modes, then use `ps/logs/inspect/stop/rm` |
| Docker-owned MicroVM lifecycle | [`docker-microvm`](docker-microvm) | Register an opt-in runtime and administer the same native-KVM lifecycle |
| containerd and Kubernetes | [`containerd-kubernetes`](containerd-kubernetes) | Install the generated CRI handler and `RuntimeClass` |
| KubeVirt plugin boundary | [`kubevirt-microvm.json`](kubevirt-microvm.json) | strict plugin configuration fixture |
| Distributed mTLS scheduling | [`distributed`](distributed) | Start a control plane and capability-aware worker |
| Reproducible builds and CAS reuse | [`reproducible-build`](reproducible-build) | See a second pipeline run reuse the first run's cache |
| Structured events and journals | [`observability`](observability) | Follow one build through JSONL events and its journal |

Examples use placeholders where credentials, immutable registry digests,
kernels or certificates are necessarily environment-owned. Replace only the
explicit `REPLACE_*` values; never weaken digest pinning or TLS verification.

The automated test in this directory only prevents examples and fixtures from
silently becoming stale. These files are written for people first.

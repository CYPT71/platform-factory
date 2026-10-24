# Security maintenance validation — 2026-10-24

The pinned compiler is now Go 1.26.10, including all workspace modules, generated
plugin module templates, the developer bootstrap version and the multi-platform
dev-container digest. A scan with the older Go 1.25.12 toolchain found callable
standard-library vulnerabilities; the corrected compiler scan no longer reports
them. Official compiler releases are listed at [go.dev](https://go.dev/dl/).

The containerd integration now pins containerd 2.2.9 and gRPC 1.83.2. Both the
containerd and Kubernetes modules pin x/net 0.60.0 with their selected transitive
patches. The Linux shim scan identified additional vulnerabilities hidden by a
host-only macOS scan; version selection followed the fixed versions reported by
the Go vulnerability database, not just the initial Dependabot patch floor.

Security CI uses govulncheck v1.9.0 on the root module and separately on
containerd, Kubernetes and KubeVirt with GOWORK disabled. Root-module `./...`
does not reach separate workspace modules. Runtime scans execute on Linux CI;
macOS-only results must not be presented as covering Linux build-tagged code.
The local security reproduction script uses the same module set and tool pin.

Verified locally:

- Full root tests and vet on Go 1.26.10, plus all official plugin and TUI modules.
- containerd tests with the race detector on macOS and a real Linux/arm64
  container inside the AppleHV VM; the Linux run includes the runtime shim.
- No callable vulnerability reported by the root scan or the corrected
  Linux/arm64 containerd and Kubernetes scans at validation time.
- actionlint and repository workflow validation accept the updated workflows.

This describes the scanned code, target and database state, not a guarantee
against every vulnerability. Dependabot alerts can lag manifest/snapshot updates;
none is dismissed to hide a finding. Re-run the scans as advisories and releases
change. The deployment acceptance report remains a separate functional proof.

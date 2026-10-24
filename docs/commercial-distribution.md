# All-in-one distribution

Platform Factory ships the CLI, eight language plugins (Go, Python, Node,
Java, .NET, Rust, Ruby and PHP), OCI packaging, native VM support, distributed
workers and the Kubernetes, KubeVirt and containerd integrations together.
Language plugins inspect/package projects; interpreters, compilers, cluster
credentials and Linux guest images remain explicit prerequisites. Installing a
plugin does not install its toolchain or grant access to a cluster.

## Install from source

Run either installer from this checkout with the pinned Go toolchain:

```sh
./install.sh --components all --prefix ./dist/local/bin --yes
# Equivalent Go installer:
go run ./cmd/platform-factory-installer -components all -prefix ./dist/local/bin -yes
./dist/local/bin/pf version
```

Core always includes all eight language binaries. `integrations` selects the
three deployment/runtime integration binaries. `builder` includes the OCI
builder and distribution packager. The OCI runtime and containerd shim are
included only for Linux/amd64, their supported execution target. Kubernetes
and KubeVirt are activated with [signed plugin bundles](plugin-bundles.md):
`pf plugin bundle` pins the built executable and signs its manifest;
`pf plugin verify` checks it offline before installation. Shipping a binary
never relaxes publisher trust or execution isolation. See [plugin marketplace](plugin-marketplace.md) and
[containerd integration](containerd-kubernetes.md).

Native macOS builds enable CGO for Apple Virtualization.framework and apply
an ad-hoc code signature with the virtualization entitlement. This grants the
local API capability; it is not a publisher signature or Apple notarization. Cross-built
macOS binaries cannot include that native backend. Linux kernels and initramfs
are supplied separately; this package does not claim that a compile proves a
real guest boot. See [compatibility](reference/compatibility.md).

## Build and verify a relocatable release

```sh
scripts/local/bootstrap.sh --target darwin/arm64 --env ./dist/macos
# PowerShell: ./scripts/local/bootstrap.ps1 -TargetOS darwin -TargetArch arm64 -Environment ./dist/macos
go run ./cmd/platform-factory-packager --env ./dist/macos --out ./dist/macos.tar.gz
mkdir ./dist/verify
tar -xzf ./dist/macos.tar.gz -C ./dist/verify
go run ./cmd/platform-factory-packager --verify ./dist/verify
```

The verifier runs independently of the installed binaries. It rejects changed,
missing, extra, duplicate or unsafe files and symlinks. `MANIFEST.json` records
the target, version, SHA-256 and size of every packaged file. The verifier
checks integrity; authenticity requires a trusted release checksum or signature
obtained separately. Never treat an attacker-supplied manifest as a trust anchor.
Do not execute an extracted binary until verification has succeeded.

Release CI builds and verifies the extracted package on macOS, Linux and
Windows before executing the CLI. Linux/arm64 is also packaged; the Linux/amd64
runtime/shim are deliberately absent from that target.

## Acceptance and commercial scope

The delivery contract is: one install selection builds the supported official
components; no missing plugin requires a customer to discover a separate Go
module; every archive can be checked before execution; existing plugin trust
and native Apple virtualization behavior are preserved.

This is a distributable product foundation, not a claim of production or
commercial completion. Remaining commercial decisions include pricing,
licensing/support terms, release signing ownership and customer acceptance.
The compiled-service path now has [real cluster acceptance](local-kubernetes-acceptance.md)
for signed reproducible publication, scaling, logs, redeployment and repeated
rollback. Technical work still includes the interpreted-language runtime supply
chain and real customer acceptance beyond this controlled cluster scenario. [TODO review](../TODO-REVIEW.md) tracks those gaps. No external
release, paid service, cluster deployment or publication is performed by these
local build commands.

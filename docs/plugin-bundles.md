# Signed, offline-installable plugin bundles

`pf plugin bundle` turns an existing `plugin.json` template and a prebuilt
executable into a signed installation unit. It computes the digest of the bytes
actually copied, preserves the plugin identity/capabilities/permissions, signs
that metadata with Ed25519 and exports the public key. It never executes plugin
code, invokes a compiler, fetches dependencies or changes cluster resources.

The official Kubernetes and KubeVirt plugins are framed-RPC plugins supported
by this workflow. Language command plugins continue to use `pf plugin load`.
The containerd configuration CLI and runtime shim are installed as executables,
not passed off as framed-RPC plugins.

## Publisher workflow

First build the all-in-one distribution as documented in
[commercial distribution](commercial-distribution.md). From the source checkout:

```sh
# Use a private directory outside every distributable output.
pf plugin bundle \
  --from ./plugins/kubernetes \
  --executable ./dist/macos/bin/platform-factory-kubernetes \
  --output ./dist/plugin-bundles/kubernetes \
  --key-dir "$HOME/.platform-factory/publisher-keys" \
  --key-name release \
  --format json
```

This explicitly creates the named persistent key on first use and reuses it
thereafter. Keep the private directory outside version control, release archives
and shared directories. The bundle contains exactly the executable,
`plugin.json` and `publisher.pem`; no private key is copied. An existing output
is refused. The manifest is published last so discovery cannot treat an
incomplete directory as an installed plugin. Different publishers use different
key directories or names. Do not treat a locally generated preview key as the
production publisher identity.

Use `plugins/kubevirt` and `platform-factory-kubevirt` for the KubeVirt bundle.
Build the executable for the recipient's host OS/architecture before packaging;
bundling does not cross-compile it or prove cluster compatibility.

## Verify and install

Obtain the publisher public key through an independently authenticated channel.
A public key received alongside an unknown bundle does not establish who its
publisher is. The exported public key is convenient for a locally generated
bundle whose source/key you already control.

```sh
pf plugin verify --from ./kubernetes --key ./trusted-publisher.pem --format json
pf plugin install --from ./kubernetes --key ./trusted-publisher.pem
pf plugin list --format json
```

Verification is read-only and runs no plugin code. Installation uses the same
signature/digest checks, then verifies the copied bytes before exposing them.
Missing/wrong trusted keys, changed executables and altered signed metadata fail
closed. `--allow-unsigned` is an explicit development exception and never
bypasses a bad signature or digest. JSON reports `signed: false` for unsigned
metadata; its digest check alone is not authentication.

Installed plugins are discovered from the managed directory by default.
`PLATFORM_FACTORY_PLUGIN_DIR` and `--plugin-dir` still select an explicit
registry. Runtime commands require `--plugin-key ./trusted-publisher.pem` to
trust the signature; install does not silently trust a key for future runs.
Sandbox and permission checks remain active. A host that cannot enforce the
plugin sandbox requires the existing explicit opt-out; bundling never enables it.
Cluster operations still require the appropriate credentials, permissions and
operation journal. See [containerd/Kubernetes](containerd-kubernetes.md).

## Proven acceptance

Tests exercise the actual publisher → offline verification → installation →
verified process handshake for the official Kubernetes and KubeVirt executables,
including negotiated capabilities. They also cover wrong/missing keys, changed
permissions and executable bytes, unsigned exceptions, signing failures,
concurrent publication, private-key containment through symlink aliases and
default managed-directory discovery. Handshake tests do not call a cluster;
these results do not claim a real production deployment.

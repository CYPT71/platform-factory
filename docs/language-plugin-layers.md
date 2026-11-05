# Language plugins

Use this guide to inspect a project, install a custom language adapter or add
plugin-owned dependency layers. For signed deployment plugins, start with
[plugin bundles](plugin-bundles.md).

## Choose the right interface

| Interface | Purpose | Activation |
| --- | --- | --- |
| Plain executable, `sdk/langplugin` | Language inspection; optional dependency layers, runtime staging and scaffolding | Bundled beside the CLI or installed with `pf plugin load` |
| Framed JSON-RPC, `sdk/plugin` | Capability-based extensions, including Kubernetes/KubeVirt deployment and migration | Signed manifest and verified bundle; see [activation](plugin-bundles.md) |

These interfaces have separate installation and trust contracts. The containerd
shim is a runtime integration, not a language-command plugin.

## Available subcommands

Every official language plugin implements `inspect`. Additional commands come
from each module's `main.go`; inspection alone does not imply runtime support.

| Plugin | `freeze` / `build-layer` | `scaffold` | `runtime` |
| --- | --- | --- | --- |
| Go | No | No | No |
| Python | Yes | Yes | Yes |
| Node.js | Yes | Yes | No |
| PHP | Yes | Yes | No |
| .NET | Yes | Yes | No |
| Java, Ruby, Rust | Yes | No | No |

`pf init` delegates source-language inspection to these executables. It
preserves ambiguity and adds language-neutral operational hints; it does not
replace a missing plugin with a built-in source-language detector.

## Install, resolve and override

The mandatory core installer includes all eight language binaries beside the
CLI. A bare `go build ./cmd/platform-factory` builds only the CLI; use the
[installer](distribution.md#install-from-source) or build the needed
plugin module separately.

Resolution in [`sdk/langplugin/registry.go`](../sdk/langplugin/registry.go) uses:

1. The managed directory: `PLATFORM_FACTORY_LANG_PLUGIN_DIR`, or
   `~/.platform-factory/plugins` by default.
2. A matching binary beside the running CLI, if the managed binary is absent.

Normal execution does not search bare `$PATH`. The explicit `plugin load`
command can locate an official binary beside the CLI or on `$PATH`, then copy
it atomically into the managed directory.

```sh
pf plugin list
pf plugin load python                         # install a managed copy
pf plugin load --from ./my-plugin-src acme     # Go module or binary
pf plugin load --from ./plugin.py acme-python  # supported script source
pf plugin unload acme-python
```

`list` includes managed and adjacent binaries. `unload` removes the managed
copy only: an adjacent bundled plugin remains available. Removing an override
therefore falls back to the bundled version; it does not disable that language.

`--from` accepts Go modules, Python, JavaScript, TypeScript, PHP and C# source
as well as binaries. Source preparation needs the corresponding toolchain;
TypeScript is transformed to JavaScript and C# is published as a self-contained
executable. Every CLI load probes `inspect` on an empty directory; failure
removes the newly installed copy.

## Add dependency layers

Set `language_plugin: true` in `pf.yaml` only for a plugin implementing
`build-layer`. During build, the host invokes the resolved executable with
`build-layer --root DIR --output TAR --dest PREFIX` and adds its tar as an
extra OCI layer. The option is off by default.

This does **not** switch dependency freezing to the executable's `freeze`
command. Project freezing uses the configured `freeze_command` or built-in
adapters; an unknown language can fall back to an RPC freeze capability.
Prepare plugin-specific dependency directories before requesting a layer.

The builder independently validates and hashes plugin layers: clean relative
paths, regular files/directories only, no links or special permission bits,
and limits of 4 GiB and 200,000 entries per layer. The shared tar writer fixes
ordering and timestamps. See [`oci/extralayers.go`](../oci/extralayers.go) and
[`LanguagePluginLayer`](../internal/app/project/language_plugin.go).

## Provision a Python runtime

An interpreted build needs a packaged Linux interpreter. The official Python
plugin implements the `runtime` command used by:

```sh
pf plugin provision-runtime --language python \
  --image 'registry.example/team/python@sha256:<64-hex-digest>' \
  --dir ./my-app --arch amd64
```

Replace the placeholder with a reviewed, real digest-pinned image. The native
Registry client selects the Linux platform and verifies content before rootfs
extraction. The plugin stages its runtime closure; the host validates and
records `runtime`, application arguments and includes, then refreshes the
adjacent lock. An existing `runtime` field makes this explicit command fail.

Interactive `pf init` can offer provisioning from a matching Linux host
interpreter or an image. Skipping that offer leaves the requirement unresolved.
Builds never start provisioning themselves. Other interpreted profiles
currently need explicitly supplied runtimes rather than this Python adapter.

## Diagnose and verify

| Symptom | Next action |
| --- | --- |
| Language plugin unavailable | Use the core installer, or build and explicitly load the module |
| Unload appears ineffective | Check for a bundled binary beside the CLI |
| Load probe fails | Fix the plugin's `inspect` output or exit status, then reload |
| Interpreted build lacks runtime | Stage the runtime explicitly; Python can use the command above |
| Dependency layer is missing | Check plugin support and its prepared dependency directory |

Run checks in each changed module; root `./...` does not include every module
in `go.work`:

```sh
cd plugins/lang-python
GOWORK=off go build ./...
GOWORK=off go vet ./...
GOWORK=off go test ./...
```

For public contract changes, also follow the
[compatibility policy](api-compatibility.md). A successful compile is not runtime
or package-manager acceptance evidence.

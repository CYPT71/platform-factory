# SDK examples

These examples are small applications, not validation fixtures. Each
imports only a supported `sdk/` package (or, for non-Go languages, the
equivalent local package under `sdk/plugin-*`) and is exercised by
`run.sh` / the normal Go test suite.

| Goal | Example | Run |
| --- | --- | --- |
| Load and inspect a pipeline | [`pipeline`](pipeline) | `go run ./examples/sdk/pipeline examples/pipeline.json` |
| Build a secure OCI image, with or without the MicroVM | [`oci`](oci) | `go run ./examples/sdk/oci` |
| Validate a MicroVM specification | [`microvm`](microvm) | `go run ./examples/sdk/microvm` |
| Implement a language plugin (Go) | [`plugin-go`](plugin-go) | Build it, then run `platform-factory-conformance plugin BINARY` |
| Implement a language plugin (Python) | [`plugin-python`](plugin-python) | `platform-factory-conformance plugin plugin-python/plugin.py` |
| Monitor Docker/Podman with the Python SDK | [`plugin-python-lazy-docker`](plugin-python-lazy-docker) | Conformance runner + Python unit tests |
| Monitor Docker/Podman with the Go SDK | [`plugin-go-lazy-docker`](plugin-go-lazy-docker) | Build + conformance runner |
| Monitor Docker/Podman with the JavaScript SDK | [`plugin-javascript-lazy-docker`](plugin-javascript-lazy-docker) | Conformance runner |
| Monitor Docker/Podman with the TypeScript SDK | [`plugin-typescript-lazy-docker`](plugin-typescript-lazy-docker) | `npm run build` + conformance runner |
| Monitor Docker/Podman with the C# SDK | [`plugin-csharp-lazy-docker`](plugin-csharp-lazy-docker) | `dotnet build` + conformance runner |
| Implement a language plugin (JavaScript) | [`plugin-javascript`](plugin-javascript) | `platform-factory-conformance plugin plugin-javascript/plugin.js` |
| Implement a language plugin (TypeScript) | [`plugin-typescript`](plugin-typescript) | `npm install && npm run build`, then `platform-factory-conformance plugin plugin.js` |
| Implement a language plugin (C#) | [`plugin-csharp`](plugin-csharp) | `dotnet publish -c Release -o OUT`, then `platform-factory-conformance plugin OUT/SecureOciPlugin` |

Every plugin example is validated by the exact same external suite
(`platform-factory-conformance plugin`): the v1 handshake, required
capabilities, valid `detect`/`freeze`/`plan` responses, unknown-method
handling, framing, and the 1 MiB limit. A production `plugin.json`
additionally pins the executable with its exact `sha256:<64 hex>`
digest.

## Plugin SDKs

Each non-Go plugin example imports a real, importable local package -
not hand-rolled protocol code - the same way `plugin-go` imports
`sdk/plugin`:

| Language | Package | Location |
| --- | --- | --- |
| Go | `sdk/plugin` | [`sdk/plugin`](../../sdk/plugin) |
| Python | `platform-factory-plugin` (`secure_oci_plugin`) | [`sdk/plugin-python`](../../sdk/plugin-python) |
| JavaScript/TypeScript | `@platform-factory/plugin-sdk` | [`sdk/plugin-js`](../../sdk/plugin-js) |
| C# | `SecureOci.Plugin` | [`sdk/plugin-csharp`](../../sdk/plugin-csharp) |

All four speak the exact same versioned, length-prefixed JSON-RPC wire
protocol (`application/vnd.platform-factory.rpc.v1+json`) and the same
`v1.hello`/`v1.detect`/`v1.freeze`/`v1.plan` handshake and dispatch -
a plugin written against any one SDK is indistinguishable on the wire
from a plugin written against any other.

The `sdk/plugin-python`, `sdk/plugin-js`, and `sdk/plugin-csharp`
packages are locally importable (a source checkout, an npm/pip local
path, or a .NET `ProjectReference`) rather than published to
PyPI/npm/NuGet - they are structured so publishing later needs no
rework, not so they are installable from a registry today.

All SDKs expose native contextual handlers for arbitrary capability families:
Go `RegisterTyped`, Python `handle_context`, JavaScript/TypeScript
`handleContext`, and the C# contextual `Handle` overload.

Applications use `sdk/` for behavior and the versioned `api/*/v1` packages for
wire contracts. `api/microvm` and `api/vmm` remain migration-only aliases.

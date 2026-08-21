# Lazy Docker/Podman plugin — Python SDK example

This is a real out-of-process PF plugin built with
[`sdk/plugin-python`](../../../sdk/plugin-python/). It does not implement the
wire protocol itself: `secure_oci_plugin.Server` owns framing, the `v1.hello`
handshake, capability advertisement, dispatch and RPC errors.

The plugin advertises two useful read-only runtime capabilities:

- `runtime.status`: normalized Docker/Podman container inventory;
- `runtime.logs`: bounded container log retrieval.

It also exposes the baseline `detect`, `freeze` and `plan` handlers so the same
cross-language conformance runner can validate it:

```bash
go run ./cmd/platform-factory-conformance plugin \
  ./examples/sdk/plugin-python-lazy-docker/plugin.py
python3 -m unittest discover -s examples/sdk/plugin-python-lazy-docker -v
```

In a separately packaged plugin, replace the source-tree path shim with:

```bash
python3 -m pip install /path/to/platform-factory/sdk/plugin-python
```

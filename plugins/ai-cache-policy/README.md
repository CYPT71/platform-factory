# Cache policy validation

Requires tenant scope and a cache lifetime between 1 and 86400 seconds.

This offline JSON analyzer checks the supplied document. It does not inspect live services or enforce runtime settings.

Use the `name` field in [plugin.json](plugin.json) as `NAME` below, from the repository root:

```sh
pf extensions inspect NAME
pf extensions analyze NAME --input document.json
```

The matching profile in [the catalog](../../internal/extensions/catalog.json) defines the accepted fields, bounds, and accepted and rejected examples.

Accepted document:

```json
{
  "scope": "tenant",
  "ttl_seconds": 3600
}
```

Rejected document:

```json
{
  "scope": "global",
  "ttl_seconds": 0
}
```

The shared implementation is in `internal/extensions`. The native RPC method is `v1.analyzer.scan`; its parameters contain the input under `document`.

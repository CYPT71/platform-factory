# Structured output validation

Requires an object schema, rejects additional properties and requires at least one mandatory property. This checks the policy document; it does not validate a response against the schema.

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
  "type": "object",
  "additionalProperties": false,
  "required": [
    "answer"
  ]
}
```

Rejected document:

```json
{
  "type": "object",
  "additionalProperties": true,
  "required": []
}
```

The shared implementation is in `internal/extensions`. The native RPC method is `v1.analyzer.scan`; its parameters contain the input under `document`.

# Input and output budgets

Requires an input budget between 1 and 32768 tokens and an output budget between 1 and 8192 tokens.

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
  "max_input_tokens": 8000,
  "max_output_tokens": 2000
}
```

Rejected document:

```json
{
  "max_input_tokens": 200000,
  "max_output_tokens": 100000
}
```

The shared implementation is in `internal/extensions`. The native RPC method is `v1.analyzer.scan`; its parameters contain the input under `document`.

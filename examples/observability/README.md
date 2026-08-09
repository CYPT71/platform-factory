# Structured observability

Use this when integrating platform-factory with a log collector or CI evidence
store. No telemetry backend is required: outputs are ordinary JSON files.

OCI construction emits JSONL events carrying a trace identifier, while a
pipeline run writes a versioned result journal:

```sh
PLATFORM_FACTORY_TRACE_ID=example-trace \
  platform-factory build --config examples/platform-factory.json \
  -o /tmp/example-layout BINARY 2> /tmp/build-events.jsonl

platform-factory pipeline run --workdir /tmp/example-pipeline examples/pipeline.json
platform-factory publish --dry-run --journal /tmp/example-pipeline/journal.json \
  --policy examples/supply-chain/policy.json \
  --evidence examples/supply-chain/evidence.json \
  /tmp/example-layout registry.example/service@sha256:REPLACE_WITH_64_HEX_DIGEST
```

Events and journals intentionally exclude secret values. Provenance consumes
the journal only after strict decoding, size limits and secret-like field
rejection.

Expected result: every JSONL line carries `example-trace`, and
`journal.json` contains one terminal state per pipeline stage.

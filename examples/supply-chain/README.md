# Native supply-chain evidence

Use this after producing a verified layout. The generation steps are local;
actual publication additionally needs registry credentials and an immutable
destination.

Generate pipeline evidence and an SBOM without invoking an external SBOM or
signing engine:

```sh
platform-factory evidence --reproducible examples/pipeline.json > /tmp/evidence.json
platform-factory sbom examples/hello-pipeline/app > /tmp/sbom.cdx.json
```

For publication, use an immutable image reference and the strict policy in
this directory:

```sh
platform-factory publish --dry-run --sign --sbom \
  --journal WORKDIR/journal.json \
  --policy examples/supply-chain/policy.json \
  --evidence examples/supply-chain/evidence.json \
  LAYOUT registry.example/service@sha256:REPLACE_WITH_64_HEX_DIGEST
```

`evidence.json` is a schema-valid demonstration fixture. Its subject digest
must be replaced by evidence derived from the layout being published; the
publisher rejects a mismatch.

Start with `--dry-run`. Expected result: platform-factory explains the publication
plan and policy decision without changing the registry.

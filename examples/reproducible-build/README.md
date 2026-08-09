# Reproducible execution and CAS reuse

This example needs only Go and write access to `/tmp`. It deliberately uses
one cache directory across two otherwise independent work directories.

Plan once, then run twice against the same explicit content-addressed cache:

```sh
platform-factory pipeline plan examples/pipeline.json
platform-factory pipeline run --workdir /tmp/platform-factory-run-1 \
  --cache /tmp/platform-factory-cache examples/pipeline.json
platform-factory pipeline run --workdir /tmp/platform-factory-run-2 \
  --cache /tmp/platform-factory-cache examples/pipeline.json
```

The second journal records cache hits for stages with identical canonical
inputs. Output blobs remain addressed by digest in the shared cache.

For a standalone executable, `platform-factory build --rebuild=2
--require-identical` performs two clean builds and rejects any byte-level
layout divergence.
